# IM 模块与功能设计：业界最佳实践调研

> 调研时间：2026-11-16　|　目标：为 `rym`（单体起步的 IM 服务端）提供设计依据
>
> **本文不是原创架构，而是对公开资料的归纳 + 取舍判断。**
> 每个结论都标注了来源；标注「本项目决定」的地方才是需要落到 `rym` 里的决策。
>
> 读法建议：第 2 章是全局地图，第 4/6/7/8 章是必须一次设计对的核心，
> 其余章节可在对应功能开工前再读。

---

## 1. 一句话前提

IM 看起来是「收发消息」，实际上是四件事同时成立：

1. **一条消息不能丢**（持久化先于 ACK）
2. **同一会话内不能乱序**（服务端统一发号，不信任客户端时间）
3. **客户端能判断"我缺了哪条"**（会话内连续序号 + 补洞）
4. **连接是有状态的**（消息要找到人当时连在哪台机器）

凡是绕开这四条的"简化"，最后都要重写。

---

## 2. 一页速览：六个绕不开的硬问题

| # | 问题 | 关键结论 | 详见 |
|---|---|---|---|
| 1 | **消息 ID 与顺序号要拆开** | `msgId` 管唯一（UUIDv7/ULID），`seq` 管会话内顺序；一个字段同时满足两者在分布式下做不到 | §4.1 |
| 2 | **`seq` 必须会话内连续** | 只有连续，客户端才能靠"跳号"发现漏消息；雪花/全局自增都做不到 | §4.2 |
| 3 | **重发要幂等** | 靠客户端生成的 `client_msg_id` + 唯一索引，不能靠"再分配一个 seq" | §4.3 |
| 4 | **同步是三条游标，不是一条** | 消息 seq、会话版本号、已读游标，三者语义不同、分别同步 | §6 |
| 5 | **群消息扇出要按规模分档** | 千人以下写扩散，五千以上切读扩散；一刀切必然在某个规模崩 | §8 |
| 6 | **长连接网关是有状态的** | 路由表（uid → gateway）是核心拼图，滚动发布要做连接排水 | §9 |

---

## 3. 模块能力地图

把 IM 拆成九块，每块都能独立演进的：

```
                        ┌─────────────────────────┐
   客户端 ──长连接──►   │ ① 接入层 gateway         │  有状态：连接、心跳、路由
                        └───────────┬─────────────┘
                                    │ 内部 RPC / MQ
                        ┌───────────▼─────────────┐
                        │ ② 消息核心 message       │  发号、落库、幂等、撤回
                        └───────────┬─────────────┘
                                    │
        ┌───────────────┬───────────┴──────────┬──────────────┐
        ▼               ▼                      ▼              ▼
 ③ 会话 conversation  ④ 关系 friendship/group  ⑤ 同步 sync   ⑥ 推送 push
        │               │                      │              │
        └───────────────┴──────────────────────┴──────────────┘
                                    │
        ⑦ 媒体 media     ⑧ 搜索/审核/合规      ⑨ 管理与审计
```

| 模块 | 职责 | 关键设计点 |
|---|---|---|
| ① gateway | 维持连接、心跳、上下行转发、路由表 | 有状态、连接排水、假死连接 |
| ② message | 分配 seq、落库、幂等、扇出触发 | 发号与去重分离、事务边界 |
| ③ conversation | 会话列表、未读、置顶、免打扰 | 每人一行 participant 状态 |
| ④ user/group | 用户、好友、群、成员角色 | 成员有效期影响可见范围 |
| ⑤ sync | 冷启动/重连/补洞/多端 | 三条游标 |
| ⑥ push | 在线推送、离线推送、三方通道 | 幂等、可降级 |
| ⑦ media | 图片/语音/文件 | 消息只存引用，不塞字节 |
| ⑧ search/moderation | 全文搜索、内容审核 | 与主链路异步解耦 |
| ⑨ admin/audit | 管理后台、审计、合规归档 | 只读副本，不与主库争抢 |

来源：[Wener 笔记：IM 设计](https://wener.me/notes/dev/design/im)、[实时聊天系统模板](https://github.com/study8677/awesome-architecture/blob/main/templates/realtime-chat/README.md)

---

## 4. 消息模型

### 4.1 `msgId` 与 `seq` 必须分开

这是全网最一致的一条结论。

| 字段 | 要求 | 典型做法 |
|---|---|---|
| `msg_id` | **全局唯一**，不需有序 | UUIDv7 / ULID（字典序≈时间序，便于分片与排查） |
| `seq` | **会话内单调、最好连续**，不需全局唯一 | 服务端按会话分配 |

微信的做法是把「消息 ID」拆成 `msgId` + `seqId`：msgId 只保证唯一，seqId 只保证顺序
（[微信 seqsvr 分享](https://cloud.tencent.com/developer/article/1352896)）。
Tencent Cloud 的对比文章也给出同一结论：**雪花适合当实体 ID，不适合当消息序号**——
因为增量同步要求「服务端拿到客户端报的 N，能算出该从哪条往后捞」，雪花在会话内是稀疏的，算不出来
（[三种发号方案比较](https://developer.cloud.tencent.cn/article/2683882)）。

> **反例**：把雪花直接当 `seq`。后果是客户端看 `seq` 从 8801 跳到 9532，
> 无法区分"别的会话占号"还是"自己漏了"，只能保守触发全量拉取 —— 成千上万客户端同时回源，
> 直接把历史存储打穿（拉取风暴）。

### 4.2 `seq` 怎么分配：三种方案

| 方案 | 实现 | 优点 | 致命缺陷 |
|---|---|---|---|
| **DB 行锁自增**（推荐起步） | `UPDATE conversations SET last_seq = last_seq + 1 WHERE id = ? RETURNING last_seq`，与 `INSERT message` 同事务 | 无额外组件；行锁保证"分配序 = 提交序" | 单会话写入串行；热点群受单行锁限制 |
| Redis `INCR` | `INCR seq:<conv_id>` | 快 | 与落库不同事务 → 可能产生**永久空洞**；Redis 丢数据会回退 |
| 号段/步长批发 | 本地缓存一段号，用完再取 | 吞吐最高（微信 seqsvr 思路） | 重启浪费号段；需处理空洞语义 |

**关键机理（容易被忽略）**：如果 `seq` 在事务之外分配，会出现

```
T1 拿到 101（未提交）
T2 拿到 102（先提交）
客户端拉 after=100 → 只看到 102，把游标记成 102
T1 提交 → 101 永远拉不到了
```

DB 行锁方案天然避免：同一会话的写者必须排队，**分配序 == 提交序**
（[深入理解 IM 系统·顺序](https://im.liko.page/zh/ordering/)）。

**关于空洞**：即便用行锁，事务回滚也会留下空号。客户端发现跳号先等一个短窗口（如 500ms）再补拉，
补拉时服务端如果确实没有该号，必须明确回答「该 seq 不存在」，客户端跳过而不是无限重试。

> **本项目决定（v1）**：用 `conversations.last_seq` 行锁自增 + 同事务插入消息，
> `(conversation_id, seq)` 建唯一索引。不做号段批发——单体阶段没有那个吞吐压力，
> 而空洞语义是额外的复杂度。

### 4.3 幂等：靠 `client_msg_id`

客户端发消息前生成一个稳定的 UUID，重发时**复用同一个**。

```sql
-- MySQL：唯一键做闸门
UNIQUE KEY uk_conv_client (conversation_id, sender_id, client_msg_id)

INSERT INTO messages (...) VALUES (...)
ON DUPLICATE KEY UPDATE id = id;   -- 不产生第二行
-- 然后按 (conversation_id, sender_id, client_msg_id) 回查已存在的 seq 返回
```

响应必须返回**首次分配的那个 `seq`**，而不是重新分配。

来源：[IMCore：为什么消息不丢不乱序](https://balala.im/docs/why-messages-not-lost/)、[会话、消息与序号](https://plumephp.com/inet-social-08-1-session-message-seq/)

### 4.4 消息字段建议

| 字段 | 说明 |
|---|---|
| `id` / `msg_id` | 全局唯一（UUIDv7 或 BIGINT 雪花） |
| `conversation_id` | 所属会话 |
| `seq` | 会话内序号 |
| `sender_id` | 发送者 |
| `client_msg_id` | 幂等键（客户端生成） |
| `type` | `text` / `image` / `file` / `audio` / `video` / `system` / `custom` |
| `body` | 结构化 JSON（type 不同结构不同，为富文本/卡片/Bot 留扩展） |
| `status` | `normal` / `recalled` / `deleted` / `blocked` |
| `reply_to` | 引用回复的消息 ID |
| `created_at` | 服务端落库时间（**排序不依赖它，但展示要用**） |
| `edited_at` / `recalled_at` | 生命周期时间 |

**不要**：用客户端时间排序；把图片字节存进消息体；用 `status` 一个字段混装
"发送中/已送达/已读/已撤回"（前三个是传输态，后两个是内容态，混在一起必然打架）。

### 4.5 可变动作要事件化

编辑、撤回、删除、表情回应，**不要直接 UPDATE 消息体了事**。正确做法是产出事件：

```
MessageCreated / MessageEdited / MessageRecalled / MessageDeleted / ReactionAdded
```

原因：多端要同步、审计要留痕、客户端要能按事件幂等地打补丁（带 `version`，旧版本丢弃）。

来源：[Wener 笔记](https://wener.me/notes/dev/design/im)、[Design a chat app](https://completefrontend.com/system-design/problems/chat-app)

---

## 5. 会话模型

### 5.1 会话是核心聚合根

> 「会话不只是聊天窗口，它也是组织消息、未读状态与同步进度的边界。」
> —— [以会话为核心设计 IM](https://juejin.cn/post/7701896651883708416)

不要把 `Chat` / `Group` / `Room` 写死成互斥模型。统一抽象：

```
Conversation（共享的消息容器）
    └── Participant（每个用户一份个人状态）
```

**群消息只存一份在 Conversation 里；每个人的已读、置顶、免打扰在 Participant 里。**

### 5.2 会话 ID 生成

| 类型 | 生成方式 |
|---|---|
| 单聊 | 双方 uid 字典序排序 + 拼接 + 哈希，加类型前缀。保证 A→B 与 B→A 得到同一个 ID，**不需要先查"是否已有会话"** |
| 群聊 | 服务端创建时生成，成员变化不改 ID |
| 系统会话 | 按业务键，如 `system:order:123` |

来源：[会话、消息与序号](https://plumephp.com/inet-social-08-1-session-message-seq/)

### 5.3 Participant 字段（每个人一行）

| 字段 | 用途 |
|---|---|
| `last_read_seq` | 已读游标（只增不减） |
| `last_delivered_seq` | 已投递位（用于"已送达"回执） |
| `visible_from_seq` | 可见起点（入群前的历史是否可见） |
| `joined_at` / `left_at` | 成员有效期，影响已读分母与可见性 |
| `pinned` / `muted` / `tags` | 个人设置 |
| `version` | 会话状态版本号（多端增量同步用） |

> 注意：`visible_from_seq` 和 `joined_at` 是**权限**问题，不是展示问题。
> 新人能看多少历史，必须在查询时按 seq 区间裁剪，否则就是越权读。

来源：[WuKongIM 会话文档](https://docs.githubim.com/zh/guide/core-concepts/conversations/)、[Wener 笔记](https://wener.me/notes/dev/design/im)

---

## 6. 同步协议（全篇最重要）

### 6.1 三条游标，不要混用

| 游标 | 存在哪 | 语义 | 用途 |
|---|---|---|---|
| **消息 seq** | 客户端本地 | 这个会话我收到了第几条 | 补洞、拉历史 |
| **会话版本 version** | Participant 行 | 我的会话列表变了第几版 | 增量同步会话列表 |
| **已读游标 read_seq** | Participant 行 | 我读到第几条 | 未读数、已读回执 |

**混用会导致经典 bug**：用消息 seq 当会话列表游标 → 只改了置顶没发消息，
另一台设备永远同步不到。

### 6.2 三种恢复路径

| 场景 | 动作 |
|---|---|
| **冷启动** | 拉会话列表（带 `last_msg`、`unread`、`read_seq`、`version`）→ 渲染首屏 → 进入某会话再拉该会话最近 N 条 |
| **在线收消息** | 推送带 `seq`。若 `seq == local_max + 1` 直接追加；若跳跃，先等 ~500ms 再拉 `[local_max+1, seq]` |
| **重连** | 心跳带回服务端各会话最大 seq → 对比本地，落后的会话逐个补拉；再拉会话列表的 version 增量 |

**关键点：客户端只信任"自己亲眼见过的 seq"**，不信任服务端"应该到哪了"。
服务端只从那之后补 —— 没有模糊地带（[IMCore](https://balala.im/docs/why-messages-not-lost/)）。

OpenIM SDK 的做法是可参考的实现：心跳里拿 `SvrMaxSeq`，与 `LocalMaxSeq` 比对，
缺口区间 `[local+1, svr]` 主动拉取（[OpenIM SDK 剖析](https://segmentfault.com/a/1190000041568001)）。

### 6.3 不要用 `OFFSET` 翻历史

```sql
-- 错：深分页越翻越慢，且新消息插入会导致重复/跳过
SELECT * FROM messages WHERE conversation_id = ? ORDER BY seq DESC LIMIT ? OFFSET ?

-- 对：游标
SELECT * FROM messages
WHERE conversation_id = ? AND seq < ?
ORDER BY seq DESC LIMIT ?
```

### 6.4 冷启动不要拉全量

会话列表只需要"摘要 + 未读 + 最新一条"，不要返回完整历史。
用户加入 200 个群时，全量拉取是灾难（一个 7000 条未读的群要 1.4MB，只为显示一个数字）。

来源：[深入理解 IM 系统·未读数](https://im.liko.page/zh/unread/)、[以会话为核心](https://juejin.cn/post/7701896651883708416)

---

## 7. 未读与已读

### 7.1 三种模型

| 模型 | 多端一致 | 可自愈 | 幂等 | 抗扇出 | 适用 |
|---|---|---|---|---|---|
| 客户端本地计算 | ✗ | ✗（重装即丢） | 靠客户端 | ✓ | 单端 / E2EE |
| 服务端裸计数器 `+1` | ✓ | ✓ | ✗（重投会多加） | △ | 单聊/小群 |
| **水位 + 计数器混合** | ✓ | ✓ | ✓（水位只增） | △ | **中大规模，推荐** |

（[IM 未读数三种一致性模型](https://developer.cloud.tencent.com/article/2699432)）

**裸计数器的两个死穴**：

1. 待办重做（at-least-once 消费）会让 `+1` 执行两次，未读虚高，且**算不回来**；
2. "清零"晚到：读完合上盖子，之后又来 3 条，清零到了把 3 条也清掉。

**混合模型**：`read_seq`（水位，只增不减）是真相，`unread_count` 是加速缓存。
任何时刻都能用 `read_seq` 重算，所以计数器错了可以自愈。

### 7.2 未读数的正确算法

```
未读 ≠ max_seq - read_seq        （会把系统消息、自己发的都算进去）
```

需要两个辅助量（在分配 seq 的事务里一并写入消息行）：

- `countable_seq`：到这条为止，**该计未读**的消息累计条数（系统消息不加一）
- `sender_seq`：发送者在该会话里发的第几条

```
未读 = (最新 countable_seq − 读到位对应 countable_seq)
     − (最新 sender_seq   − 读到位 sender_seq)
```

（[深入理解 IM 系统·未读数](https://im.liko.page/zh/unread/)）

> **本项目 v1 决定**：先只支持文本/图片等"可计未读"类型，**系统消息不入消息表**（放独立事件流）。
> 这样可以先用 `countable_seq` 单量，把 `sender_seq` 留到需要精确未读时再加。
> 但**表结构要预留这两个字段**，否则以后要重算历史数据。

### 7.3 已读游标：只增不减

```sql
UPDATE conversation_participants
SET read_seq = GREATEST(read_seq, ?), version = ?
WHERE conversation_id = ? AND user_id = ?
```

- 幂等：重复上报、乱序上报都安全
- 多端：服务端记录后，要把新游标广播给同一用户的其它在线设备
- **>而不是广播"清零"这个动作**：广播"已读到 N"是声明式的，乱序到达也不会回退

### 7.4 几个必踩的边界

| 场景 | 处理 |
|---|---|
| 进群前历史 | `visible_from_seq` 初始化为入群系统消息的 seq，之前的可见但**不计未读** |
| 自己发的消息 | 不推进 `read_seq`（否则从通知栏回一句就把上面全标已读了） |
| @ 未读 | 独立索引，不能随普通未读一起清掉 |
| 手动"标为未读" | 单独一个 flag，不能把 `read_seq` 往回拨 |
| 大群 999+ | 超阈值只显示「99+」，但 **@ 必须精确** |
| 撤回的消息 | 计过未读的要扣回来（反向修正项） |
| 用户群免打扰 | 会话内仍有未读数，是否计入应用角标是产品规则 |

---

## 8. 群消息扇出

### 8.1 两种模型与真实账单

| | 写扩散（fan-out on write） | 读扩散（fan-out on read） |
|---|---|---|
| 存储 | 每条消息按成员数复制 | 群只存一份 |
| 写成本 | O(N)：千人群 = 999 次写 | O(1) |
| 读成本 | O(1)：拉自己收件箱 | O(M)：加入 M 个群要扫 M 个 |
| 已读/撤回 | 好做（每成员一份） | 难做（要做聚合） |
| 适合 | 千人群以下、toB（要已读回执） | 万人群、广播型 |

**写扩散的真实成本**：一条 7 字消息在千人群里，副本 999 次 + 路由查询 + 未读写入 + 三方推送触发
≈ **3000~5000 次后端操作**（[千人群扇出账单](https://cloud.tencent.com/developer/article/2669803)）。

贝壳的实测：300 人群、发送 300 QPS 时，写扩散在 Redis 层就是 `300×300 = 90000 QPS`
（[贝壳群聊优化](https://www.cnblogs.com/papering/p/15852645.html)）。

### 8.2 分档策略（工程上的标准答案）

| 群规模 | 策略 |
|---|---|
| < 500 | 朴素写扩散 |
| 500 – 2000 | 批量写扩散（把 999 个目标打包成 ~20 条 MQ 消息，每批 50 人） |
| 2000 – 5000 | 批量 + 在线优先（先扇在线，离线异步补） |
| > 5000 | 切读扩散 / 广播模式 |

**分流点要在建群时标记群类型，不要每次发消息动态判断**——后者会让代码路径越来越复杂。

来源：[千人群扇出账单](https://cloud.tencent.com/developer/article/2669803)、[网易万人群方案](https://netease.im/dev-blog/618)

### 8.3 双轨存储与副本指纹

写扩散的一个隐蔽问题：副本要不要进历史库？

| 数据 | 入历史库 | 入离线盒子 |
|---|---|---|
| 原消息 | ✅ | ❌ |
| 副本（每成员一份） | ❌ | ✅ |

判断靠「副本指纹」——副本 ID 里带标记（如 `uuid|G|member_id`），历史库消费者扫一眼就知道
该丢弃还是入库。这是极简但关键的工程技巧。

### 8.4 混合模型（如果将来要支持超大群）

JuggleIM 的做法值得记：

1. **会话列表用写扩散**（每人一行，只存最后一条消息 + 未读 + 已读位），存储成本极低
2. **群消息用读扩散**（群内只存一份，靠会话索引驱动拉取）
3. 在线成员优先推送，离线成员批量合并

这样单节点能撑十个以上十万人群（[JuggleIM](https://juggle.im/blog/2025-01-15-group-chat-write-fanout-vs-read-fanout/)）。

> **本项目 v1 决定**：**不做扇出**。因为第一版只有单聊 + 小群（≤200 人），
> 且是单体：`message` 写完直接在内部分发，无 MQ。
> 但**必须现在就把 `conversation` 的 `type` 和 `max_seq` 留好**，
> 让"按群规模分流"以后是一个 if 分支，而不是一次重构。

---

## 9. 长连接网关

### 9.1 它为什么特殊

HTTP 的"无状态 + 水平扩容"在这里全部失效：

- 连接是**有状态**的（某条连接物理挂在某台网关）
- 单机连接数有上限（C100K / C1000K 问题）
- 推送必须先回答"这个人在哪台机器上"

**核心拼图是路由表**：`uid → gateway 实例`，放 Redis 或网关集群注册表。
多端在线时一个 uid 对应多条记录，推送要做扇出。

来源：[长连接网关设计：万级到千万级](https://developer.cloud.tencent.com/article/2764123)

### 9.2 单机上限的现实值

| 资源 | 说明 |
|---|---|
| 内存 | 每连接缓冲区调优后可压到 20–50KB |
| fd | `ulimit` + 内核参数 |
| CPU | 收发包 + 协议处理 |

裸 TCP + epoll 能到百万级，但**带 WebSocket 握手/心跳/TLS 后，工程上按 10–50 万/台规划更稳**。

调优点：关 Nagle、调 `tcp_rmem/wmem`、`SO_REUSEPORT` 多进程监听。

### 9.3 心跳：三方约束互相打架

心跳要同时满足：

- **及时性**：死连接要分钟级发现（要求密）
- **轻量性**：不能太频繁（费电、费流量、压服务端）
- **准确性**：不能误杀只是切后台的活连接（要求疏）

工程取值参考：

| 状态 | 心跳间隔 | 服务端判定超时 |
|---|---|---|
| 前台活跃 | 15–30s | 45–90s |
| 后台 | 60–120s | 180–360s |
| 弱网 | 30–60s | 90–180s |

**规则：服务端读超时必须是心跳间隔的 2–3 倍**，否则一次抖动就误杀。

**心跳间隔必须小于链路最短空闲超时**（运营商 NAT 常见 5 分钟左右，
部分云 LB 的 idle timeout 只有 60s）——两者取小，见
[长连接上的计时器清单](https://connproof.com/network/long-connections)。

### 9.4 心跳照不到的四个盲区

1. **假死**：对端 App 冻死但内核还在回 ACK，`write` 成功 ≠ 对端收到
2. **NAT 提前回收**：两端 TCP 状态机都还是 `ESTABLISHED`，中间路已断
3. **单向坏**：上行正常下行坏，单向心跳探测的恰好是好的一边
4. **参数配比不当**：间隔/超时太近会误杀，太远会滞留

补法：
- **双向确认**（客户端靠收到回应判活，不是靠"发出去了"）
- **两段式判死**：第一次超时先补发探测，再等一个短窗口，二次无响应才断
- **多信号投票**：心跳空闲 + 写失败计数 + TCP reset。确凿信号（reset）立即判死，模糊信号走二次确认

来源：[心跳能发现死连接吗](https://developer.cloud.tencent.com/article/2682625)

### 9.5 判死清理必须幂等

判死 → 清理本机注册 → 清在线状态 → 通知路由层，这条链可能和"重连成功"并发。

**必须有连接唯一标识（建连时间戳 / session id），清理时只清"确实是我这条连接"的状态。**
否则会出现"旧连接的清理把新连接的在线状态删掉"，刚上线又变离线。

### 9.6 发布与重连

- **连接排水**：滚动发布时先停止接收新连接 → 等待存量连接自然退出 → 超时才强制迁移
- **重连退避 + 抖动**：1s → 2s → 4s → … 上限 60s，加随机抖动，避免网络恢复瞬间的重连风暴
- **重连后必须补拉**：断线窗口内的推送会丢

来源：[长连接稳定性工程实践](https://netease.im/dev-blog/698)

---

## 10. Presence 与「正在输入」

### 10.1 Presence 允许最终一致

在线状态变化极其频繁（切前后台、断网、心跳都变），强一致代价极高且没有收益。
放 Redis + TTL，允许短暂不准。

**多端聚合规则**：只要还有任一设备连上就是在线的；最后一条连接断开后**延迟 ~30s** 再宣布离线，
避免刷新页面时头像闪一下。

### 10.2 扇出成本要按"注意力"算

朴素设计：把所有联系人的状态变化推给所有客户端。
量化一下：300 个联系人 × 每天 20 次状态变化 = 单人 6000 帧/天。5000 万 DAU → **35 亿帧/秒**。

正确做法：**只推"屏幕上正在显示的人"**（会话列表可见行 + 当前会话标题栏，通常 20–40 人）。
客户端上报订阅集，滚动时 500ms 防抖，重连后重新订阅。

大群（>100 人）不显示个体状态，只显示「12 人在线」的快照。

### 10.3 「正在输入」：发送端节流 + 接收端过期

- **发送端**：最多每 3 秒发一次 `typing`，**不发"停止"帧**
- **接收端**：收到后显示 5 秒，超时自动消失；或收到对方消息时立即消失

不发停止帧的好处：丢包、关标签页都不会让"正在输入"永久卡住。

来源：[Design a chat app](https://completefrontend.com/system-design/problems/chat-app)

---

## 11. 消息生命周期操作

| 操作 | 语义 | 要点 |
|---|---|---|
| **撤回** | 发送者/管理员让所有人看到"已撤回" | 有时间窗（常见 2 分钟）；**消息行保留**，只改状态 + 发事件；要扣回未读 |
| **删除** | 对某个用户隐藏（单边） | 需要 per-user 的删除记录（`deleted_seqs` 或位图），不能删原消息 |
| **编辑** | 只能编辑自己发的文本 | 带 `version`，客户端按版本 upsert，旧版本丢弃 |
| **引用回复** | 新消息携带被引用消息的摘要 | 摘要**冗余存**在被引用字段里（原消息可能已撤回） |
| **转发** | 复制内容成新消息 | 注意权限：转发出去的可见性由新会话决定 |
| **表情回应** | 对某条消息的轻量互动 | 独立表/字段，不要塞进消息体；要能聚合 |
| **置顶** | 群内的置顶消息 | 单独索引，不要靠排序字段 |

**合规提醒**：撤回/删除对用户生效，但审计侧通常要保留原始内容（[Wener 笔记](https://wener.me/notes/dev/design/im)）。

---

## 12. 多端与设备

| 问题 | 做法 |
|---|---|
| 同一账号多设备 | 每设备一条连接；推送时对全部在线设备扇出 |
| 已读同步 | 服务端存一份 `read_seq`（取 max），变更后广播给其它设备 |
| 会话列表一致 | 靠 `participant.version` 增量，不靠消息 seq |
| 新设备冷启动 | 只拉会话摘要 + 最近消息，不拉全量历史 |
| 单设备踢下线 | 需要设备维度的 session 表 + 踢人事件 |

来源：[网易：多端同步一致性](https://netease.im/dev-blog/563)

---

## 13. 安全与合规

| 层 | 最低要求 |
|---|---|
| 鉴权 | 连接建立时校验 token（一次性 ticket 更好，避免 token 长期暴露在 URL） |
| 越权 | 所有消息查询必须带 `conversation_id` + 成员校验 + `visible_from_seq` 裁剪 |
| 敏感信息 | 日志不打消息正文；错误不泄漏内部细节 |
| 内容审核 | 异步旁路（不能阻塞发送主链路），命中后置状态为 `blocked` 并推事件 |
| 审计 | 管理端操作独立审计表，只读副本 |
| E2EE | 服务端看不到明文 → **未读数、搜索、审核全部要在客户端做**，是另一套架构。非必要不要上 |

> E2EE 的连锁代价很大：服务端无法判断"哪条该计未读"、无法搜索、无法审核。
> 社交产品通常不开；办公 IM 用"服务端可解密 + 强审计"替代。

---

## 14. IM 特有的可观测性指标

通用 HTTP 指标不够用，这几个才是健康度：

| 指标 | 为什么重要 |
|---|---|
| **有效连接率**（心跳正常 / 总连接） | 虚高连接数会掩盖真实容量 |
| **推送到达率** | 长连接系统的核心 SLA |
| **消息端到端延迟 P95/P99** | 发送 → 对端可见 |
| **seq 空洞率** | 空洞多说明发号或事务有问题 |
| **补拉次数 / 客户端** | 突增 = 推送在丢 |
| **未读偏差**（计数器 vs 水位回算） | 长期非 0 说明计数逻辑有 bug |
| **重连成功率（按地区/运营商）** | 定位区域性网络问题 |

---

## 15. 反模式清单（逐条对照自查）

1. ❌ 用客户端时间排序
2. ❌ 用雪花/全局自增当消息序号
3. ❌ 没有 `client_msg_id`，重发就产生重复消息
4. ❌ 先 ACK 再落库
5. ❌ `seq` 在事务外分配（产生永久空洞）
6. ❌ 未区分「发送成功 / 投递成功 / 已读成功」
7. ❌ 未读数用 `max_seq - read_seq` 硬减
8. ❌ 已读游标可回退（`SET read_seq = ?` 而不是 `GREATEST`）
9. ❌ 群消息一开始就按万人群写扩散设计，或反过来按小群设计后不可扩展
10. ❌ 撤回/编辑直接改消息体，没有事件、没有版本
11. ❌ Presence 做强一致
12. ❌ 深分页用 `OFFSET`
13. ❌ 单条 `write()` 成功就认为对端收到
14. ❌ 判死清理不带连接标识（误删新连接状态）
15. ❌ 心跳只在客户端发、服务端不回（探不到单向坏）

---

## 16. 落到 `rym`：v1 决策与路线

### 16.1 现在就要做对的（不可回退）

| 决策 | 选择 | 理由 |
|---|---|---|
| 消息 ID vs 序号 | `msg_id`(UUIDv7) + `seq`(会话内) 分离 | §4.1 |
| seq 分配 | `conversations.last_seq` 行锁自增，与插入同事务 | §4.2，无额外组件 |
| 幂等 | `(conversation_id, sender_id, client_msg_id)` 唯一键 | §4.3 |
| 唯一索引 | `(conversation_id, seq)` | 冲突要立即失败而不是产生两条 |
| 历史分页 | `WHERE seq < ? ORDER BY seq DESC LIMIT ?` | §6.3 |
| 已读 | `read_seq` + `GREATEST` 只增不减 | §7.3 |
| 会话模型 | `conversation` + `conversation_participant` 两层 | §5 |

### 16.2 第一版（MVP）范围

**只做**：单聊 + 200 人以内小群 → 发消息 / 拉历史 / 未读数 / 已读。

**明确不做**（并留好扩展点）：

| 不做 | 留什么 |
|---|---|
| 群扇出 / MQ | `conversation.type` + `max_seq`，让分流是 if 分支 |
| WebSocket gateway | `message` 的 service 层已经是 proto-in/out，加 `transport/ws` 即可 |
| 端到端加密 | body 存 JSON，加 `encrypted` 字段位 |
| 消息搜索 | 消息表预留 `body_text` 冗余列，将来接全文索引 |
| 多端已读广播 | participant.version 已有，加推送即可 |
| 撤回/编辑 | 消息表预留 `status` / `edited_at` / `version` |

### 16.3 表结构草案

```sql
CREATE TABLE conversations (
  id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  conv_key       VARCHAR(64)  NOT NULL COMMENT '单聊: hash(uid_a,uid_b)；群聊: g:<gid>',
  type           TINYINT      NOT NULL COMMENT '1=单聊 2=群聊',
  last_seq       BIGINT       NOT NULL DEFAULT 0 COMMENT '发号游标',
  last_msg_id    BIGINT       NULL,
  last_msg_at    DATETIME(3)  NULL,
  created_at     DATETIME(3)  NOT NULL,
  updated_at     DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_conv_key (conv_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE conversation_participants (
  conversation_id BIGINT UNSIGNED NOT NULL,
  user_id         BIGINT UNSIGNED NOT NULL,
  read_seq        BIGINT NOT NULL DEFAULT 0,
  last_delivered_seq BIGINT NOT NULL DEFAULT 0,
  visible_from_seq BIGINT NOT NULL DEFAULT 0 COMMENT '入群前的可见起点',
  pinned          TINYINT NOT NULL DEFAULT 0,
  muted           TINYINT NOT NULL DEFAULT 0,
  version         BIGINT NOT NULL DEFAULT 0 COMMENT '会话状态版本，多端增量用',
  joined_at       DATETIME(3) NOT NULL,
  left_at         DATETIME(3) NULL,
  PRIMARY KEY (conversation_id, user_id),
  KEY idx_user_version (user_id, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE messages (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  msg_id          CHAR(32)     NOT NULL COMMENT 'UUIDv7（hex）',
  conversation_id BIGINT UNSIGNED NOT NULL,
  seq             BIGINT       NOT NULL,
  sender_id       BIGINT UNSIGNED NOT NULL,
  client_msg_id   CHAR(32)     NOT NULL,
  type            TINYINT      NOT NULL,
  body            JSON         NOT NULL,
  body_text       VARCHAR(2048) NULL COMMENT '冗余纯文本，供搜索/通知预览',
  status          TINYINT      NOT NULL DEFAULT 0 COMMENT '0=正常 1=撤回 2=删除 3=屏蔽',
  reply_to        BIGINT UNSIGNED NULL,
  version         INT          NOT NULL DEFAULT 1 COMMENT '编辑次数',
  created_at      DATETIME(3)  NOT NULL,
  edited_at       DATETIME(3)  NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_conv_seq (conversation_id, seq),
  UNIQUE KEY uk_conv_client (conversation_id, sender_id, client_msg_id),
  UNIQUE KEY uk_msg_id (msg_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### 16.4 实施顺序

```
1. 表 + migrations               ← 上面的 DDL
2. biz/message：分配 seq + 落库 + 幂等（一个事务）
3. biz/conversation：列表、未读、已读推进
4. api/im/v1/*.proto：SendMessage / PullMessages / ListConversations / MarkRead
5. transport/http：路由由 proto 生成
6. 用 curl 跑通：A 发 → B 拉 → B 已读 → A 看到已读位
7. （之后）加 gateway，把推送接上
8. （再之后）加群聊与扇出
```

### 16.5 验收标准

每条都要能在压测或单测里验证：

- [ ] 同一 `client_msg_id` 重发 100 次，只产生 1 行消息，返回同一个 `seq`
- [ ] 并发 100 条发往同一会话，`seq` 连续无空洞、无重复
- [ ] 按 `seq <` 分页翻 10 页，无重复无遗漏
- [ ] 已读上报乱序（先报 100 再报 90）后 `read_seq` 仍为 100
- [ ] 未读数在"自己发消息 / 收到系统消息 / 撤回"三种情况下都正确
- [ ] 非成员访问会话返回 403，不泄漏会话是否存在

---

## 17. 参考资料

**核心概念 / 时序与同步**
- 深入理解 IM 系统：顺序 <https://im.liko.page/zh/ordering/>
- 深入理解 IM 系统：读到哪与未读数 <https://im.liko.page/zh/unread/>
- IMCore：为什么我们的消息不丢不乱序 <https://balala.im/docs/why-messages-not-lost/>
- 会话、消息与序号 <https://plumephp.com/inet-social-08-1-session-message-seq/>
- 以会话为核心设计 IM <https://juejin.cn/post/7701896651883708416>

**发号 / 序号**
- 三种发号方案比较：为什么不能用雪花 <https://developer.cloud.tencent.cn/article/2683882>
- 微信 seqsvr 实践 <https://cloud.tencent.com/developer/article/1352896>
- 阿里云 Tablestore Timeline 消息模型 <https://help.aliyun.com/zh/tablestore/use-cases/message-system-model-in-the-modern-im-system>

**未读 / 已读**
- 已读功能的底层实现 <https://weng235.github.io/posts/im-read-receipt-architecture/>
- 未读数三种一致性模型 <https://developer.cloud.tencent.com/article/2699432>
- WuKongIM 会话与未读 <https://docs.githubim.com/zh/guide/core-concepts/conversations/>

**群扇出**
- 千人群一条消息扇出 999 次：写扩散账单 <https://cloud.tencent.com/developer/article/2669803>
- 贝壳 IM 群聊优化 <https://www.cnblogs.com/papering/p/15852645.html>
- 网易万人群技术方案 <https://netease.im/dev-blog/618>
- Write fan-out vs read fan-out（JuggleIM）<https://juggle.im/blog/2025-01-15-group-chat-write-fanout-vs-read-fanout/>

**长连接**
- 长连接网关：万级到千万级 <https://developer.cloud.tencent.com/article/2764123>
- 百度千万级统一长连接 <https://developer.aliyun.com/article/1454335>
- 心跳能发现死连接吗：五个盲区 <https://developer.cloud.tencent.com/article/2682625>
- 长连接稳定性工程实践 <https://netease.im/dev-blog/698>
- 长连接上的计时器清单 <https://connproof.com/network/long-connections>

**功能与字段清单**
- Wener：IM 设计笔记 <https://wener.me/notes/dev/design/im>
- Design a chat app（presence / typing）<https://completefrontend.com/system-design/problems/chat-app>
- 腾讯云 IM 熟人社交实践 <https://cloud.tencent.com/document/practice/269/86727>
- OpenIM 客户端 SDK 消息同步 <https://segmentfault.com/a/1190000041568001>
- 网易 IM 技术博客合集 <https://github.com/netease-im/dev-blog>
- 52im 系列（FastIM）<https://github.com/52im/fastim2023>

# AGENTS.md — rym（IM 服务端）

本文件适用于仓库根目录 `/mnt/d/Projects/rym`（module `github.com/CycleZero/rym`，分支 `master`）。
**所有 Go / make 命令必须在仓库根目录执行。**

这不是风格建议，而是本仓库已经踩过坑之后沉淀的硬约束。违反其中任何一条，评审直接打回。

---

## 0. 动手前先读什么

按顺序读，不要跳：

1. `docs/im-design-best-practices.md`
   - 重点：§4 消息模型、§5 会话模型、§6 同步协议、§7 未读已读、§16 落地决策
   - §16.3 有表结构草案，§16.5 是验收标准
2. `app/demo/` 整个目录
   - 四层结构的**活样板**：照着写，不要自己发明
   - 注意 `app/demo/provider.go` 的公开装配面（只暴露 `Module` + `ProviderSet`）
3. `pkg/architecture/architecture_test.go`
   - 这些规则由 AST 测试锁死，违反直接红
4. `README.md` 的「分层与依赖规则」「Wire 链」「IM 模块」
5. 本文件

**设计口径以 IM 设计文档为准。** 文档没写的，按第 1 节三条原则选“最简单且能解释理由”的方案，并把决定（含代价）写进注释或 commit body。
**禁止凭记忆发明业务行为。** 遇到必须偏离约束的情况，先停下来问，不要先做再说。

---

## 1. 项目定位与最高原则

- 目标形态是「企业级开源 IM 平台」（组织架构、权限、审计），但**当前阶段是学习与练手**，不上生产、不扛百万并发。
- 排序：**正确性 > 性能 > 可用性**。不做多活、容灾、灰度、容量规划。
- 单体部署，但目录与依赖形状必须保证「将来能 `mv app/<svc>` 拆成微服务」。

三条原则（比任何具体任务都重要）：

1. **宁可少一个功能，不要多一层抽象。**
2. **不为假设中的规模做优化**（不做 MQ、不做扇出、不做读写分离、不做分库分表）。
3. **每次“先这样简化”都要留明确口子**（字段 / 接口 / 类型），并写清：简化了什么、代价是什么、将来怎么补。

明确不做（不要提前实现）：

- MQ / outbox / 异步扇出
- WebSocket / 长连接 gateway / 在线推送
- 群扇出、大群、读扩散
- 端到端加密、消息搜索、撤回 / 编辑（表结构预留即可）
- 多活 / 容灾 / 灰度 / 限流
- 角色权限、组织架构、审计（属于后续阶段）

**不要改的东西：**

- `app/demo`（四层样板，保留）
- `pkg/{log,trace,metrics,response,transport,tx,db}` 的现有实现（需要改先问）
- 参考仓库 `/mnt/d/Projects/vcyuan/vcyuan-backend-app`（只读，不修改、不照抄它的部署复杂度）

---

## 2. 目录与分层（硬约束）

```
api/<svc>/v1/            对外契约（proto + 生成物），服务之间只认它
conf/                    进程级共享配置 proto
app/<svc>/
├── provider.go          公开装配面：Module + ProviderSet
├── wire.go              //go:build wireinject，服务自己的 injector
├── wire_gen.go          生成物（入库）
├── conf/                服务自有配置（拆分时整目录搬走）
└── internal/{data,biz,service,server}
cmd/server/              单体阶段唯一入口 + 组合根
pkg/                     共享基础设施
```

依赖方向：

```
cmd/server
  → app/<svc>/internal/server   （协议装配，只认识 service）
  → app/<svc>/internal/service  （proto ↔ biz，不 import data）
  → app/<svc>/internal/biz      （领域模型 + Repo 接口 + 用例，零 proto、零 gorm）
  ↑ 实现
  app/<svc>/internal/data       （Repo 实现 + PO + 事务）
```

硬规则（`pkg/architecture` 锁死，不要试图绕过）：

| 规则 | 说明 |
|---|---|
| `biz` 不 import `api/`、不 import `data` | 领域层不依赖传输与持久化 |
| `service` 不 import `data` | 薄适配层，只做 proto ↔ biz |
| 生产代码禁止 `AutoMigrate` | 表结构走 `app/<svc>/migrations/NNN_*.sql` |
| 服务之间零 import | 只能经 `api/` 契约或 `pkg/` 协作，否则拆不出去 |
| `biz` 不出现 `*gorm.DB` / `*sql.Tx` | 事务边界用 `tx.TxManager.WithTx` |
| 无包级可变全局 | 中间件顺序写死在 `pkg/transport.NewMiddlewares` |

其他分层约定：

- **service 每个 RPC 只调用一次 biz**：不允许在 service 里拼业务、循环调 repo、写事务。
- **Repo 接口定义在 biz，实现在 data**，用 `wire.Bind` 绑定（编译期 + 生成期双重校验）。
- **DTO 在 biz 定义**，与 proto message 分离；转换函数三处固定：
  - `data`：`po.toBiz()` / `fromBiz()`
  - `service`：`toProtoXxx()`
  - 每个转换配严格字段对比测试，防漏映射。
- **internal 是黑盒**：`cmd/server` 只能看到 `app/<svc>` 根包。`Provide` 的入参/返回值里出现任何 `internal/*` 类型都算泄漏（Go internal 规则会直接让组合根无法引用）。
- 不允许 `type Module = server.Module` 别名：Wire 生成的类型字符串会带上 internal 包名。

---

## 3. 配置铁律（NON-NEGOTIABLE）

**业务配置一律进 conf/proto，禁止环境变量。**

1. 进程级配置：`conf/common.proto`（server/log/otel/data/security）。
2. 服务级配置：`app/<svc>/conf/conf.proto`，文件默认 `configs/<svc>/config.yaml`。
3. 通过 Wire 注入：`Load()` 返回结构体，由组合根显式传递；**不设配置全局**。
4. 需要 `os.Getenv` / `os.LookupEnv` 读业务配置 → **不合格，直接打回**。

环境变量**只用于部署覆盖**：`config.yaml` 里写成 `${KEY:default}` 的字段才能被 `APP_KEY` 覆盖。
密钥 / 阈值 / 开关 / URL / TTL / 前缀 / 分页大小，全部进 conf，不得图省事用环境变量。

其他铁律：

- **fail-closed**：必填配置缺失时启动失败或拒绝服务，不得静默放行。
  - `security.require_auth=true` 且没有 `jwt_secret` → 启动失败。
  - 需要签发登录令牌的服务（当前 `app/user`）在 `jwt_secret` 为空时 **启动失败**，不要等到运行期登录 500。
- 服务配置文件缺失 → 单体阶段用默认值（拆分后应改为 fail-fast）。
- **`config.yaml` 已 gitignore，不要提交**；只提交 `configs/<svc>/config.yaml.example` 和根目录 `config.yaml.example`。
- 业务配置禁止用包级变量缓存；`Load` 的结果显式注入。
- 配置字段用 snake_case，与 protojson 键一一对应；缺省值在 `config.go` 的 `Validate()` 里补齐，不要在业务代码里散落 `if x == 0`。

### 配置中心（etcd）

- key 约定：`<prefix>/<env>/configs/<svc>/config.yaml`，默认即
  `rym/dev/configs/im/config.yaml`。`prefix` / `env` 在进程级 `config.yaml` 的
  `etcd` 段配置，生产必须显式设置 `env`。
- 一个 scope 一个文档、**精确 key**：不要 `WithPrefix` 扫 `configs/<svc>/`，
  否则以后新增文件会被合并，破坏“一次 PUT = 一次原子更新”。
- `etcd.endpoints` 为空 = 只用本地 `configs/<svc>/config.yaml`；配置了 etcd 后
  启动时 key 缺失/解码失败/校验失败一律启动失败（fail-closed）。
- 运行期非法更新或删除 key 保留上一版；watch 断开后全量重载一次。
- 热更新只给**每次请求读快照**的消费者：`biz` 侧用 `SettingsProvider`，
  每个用例入口读一次，不能把 `Settings` 按值存进 Usecase。
- 目前只有 im 接入；demo/user 仍启动时读本地配置，不要假设它们会热更新。
- 进程级基础设施配置（DB/Redis/监听/OTel）**不要**放进动态配置。

---

## 4. 鉴权与安全边界

**不要主动新增额外的鉴权机制。** 只实现用户明确要求的方案。当前批准的是两种：

1. 网关身份头：`trust_gateway_identity=true` 时信任 `x-md-global-auth-user-id`（生产终态）。
2. 本地 JWT：`Authorization: Bearer <token>`，**只接受 HS256**（中间件用 `jwt.WithValidMethods` 固定算法，不要放开 `alg`）。

不要擅自引入 mTLS、全接口请求签名、二次校验、会话表、设备指纹等机制。

坑与规则：

- **`require_auth=true` 是全局拦截**：它会让注册/登录这类公开接口也要求身份。只有确认部署在网关之后、所有流量都已带身份时才开；单体本地开发保持 `false`，由业务 handler 自己检查 `UserID == 0` 返回 401。
- JWT claim 契约：`user_id`（中间件同时接受数字与字符串）、`iat`、`exp`。签发与校验必须用同一个 `security.jwt_secret`。新增 claim 不影响鉴权，但不要改 `user_id` 的语义。
- **登录安全**：
  - 密码用 bcrypt（生产 cost=10），不存明文、不记日志、不下发响应。
  - bcrypt 上限 72 字节，超过必须显式拒绝（否则会被静默截断）。
  - 用户名统一小写后存储（MySQL 默认排序规则大小写不敏感，SQLite 唯一索引大小写敏感，不归一化就是两套语义）。
  - 用户不存在与密码错误返回**同一个 401 与同一条消息**；用户不存在时也执行一次 dummy bcrypt 比较，避免响应时间枚举账号。
  - 账号禁用只在密码校验通过后提示。
- IM 越权：**非成员与不存在的会话统一 403**，不要用 404/403 的差异让攻击者探测会话是否存在。所有消息查询必须先校验成员身份。
- 客户端 IP：只有 TCP 对端命中 `server.http.trusted_proxies` 才采信 `X-Real-IP` / `X-Forwarded-For`；留空表示谁都不信。**禁止无条件读代理头**（可伪造，污染日志/审计/限流）。
- CORS：只回显白名单 Origin；**绝不使用 `*` + `Allow-Credentials`**。
- pprof 只监听独立端口；`enable=true` 但 `port=0` 直接跳过，绝不挂业务端口。
- 日志禁止出现：密码、令牌、消息正文、连接串、JWT 密钥。
- 错误响应禁止透出 SQL / 连接串 / 堆栈；内部原因只进日志。

---

## 5. 响应、错误与日志

### 5.1 响应契约

成功：

```json
{"code":0,"message":"ok","data":{...},"trace_id":"..."}
```

失败（无 `data`）：

```json
{"code":40400,"message":"记录不存在","trace_id":"..."}
```

- 业务码 = HTTP 状态码 × 100（`pkg/errs`）：40000/40100/40300/40400/40900/42900/50000/50300/50400。
- 业务数据统一放 `data` 下。
- `trace_id` ≡ 响应头 `X-Request-ID` ≡ 日志 `trace.id` ≡ OTel TraceID。**不要新造第二个请求 ID。**
- 上游带 `traceparent` 时续接链路，不新起。

### 5.2 错误处理

- `biz`/`data` 返回 `*errs.Error`；`service` 不把错误翻译成 HTTP 状态码，交给 `pkg/response.EncodeError`。
- 领域错误定义在 biz（`errs.NotFound/Forbidden/Conflict/Unauthorized/...`），data 把存储错误翻译成领域错误。
- 未归一化的 error 一律 500 + 固定消息，细节只进日志。
- `context.DeadlineExceeded` → 504；`context.Canceled` → 499。

### 5.3 日志（单一出口）

**`biz` / `service` 不记错误日志。** 错误日志唯一出口是 `pkg/response.EncodeError`：

- 只有 5xx 记 `Error`；4xx 不写日志。
- 两处都记 = 一次失败两条日志，且把 404 记成 Error、制造告警噪声。

其他日志规则：

- 注释、错误消息、日志一律**中文**。
- 关键路径 / 不变量 / 取舍必须写注释，解释 **why 与代价**；不要写“给变量赋值”这种“是什么”的废话注释。
- 日志级别：Debug 调试细节、Info 关键业务路径、Warn 可恢复异常/降级、Error 仅真故障。
- **必须带 ctx**：用 `log.WithContext(logger, ctx)` 或 `log.FromContext(ctx)`，保证 `trace.id` / `span.id` 自动附加。
  - 坑：`logger.Info(...)`（不带 ctx）会丢链路字段，日志无法跳转 trace。
  - 请求路径上禁止 `context.Background()` 打日志。
- 结构化字段用 key-value，不要把变量拼进消息名。
- 热路径少用 Error；确认真故障再用。

---

## 6. IM 领域不变量（不可回退）

这些是文档 §16.1 的决策，改任何一条都等于重写：

1. **`msg_id` 与 `seq` 分离**
   - `msg_id`：UUIDv7 hex（32 字符），全局唯一，无顺序语义。
   - `seq`：会话内连续序号，服务端分配，客户端靠跳号发现漏消息。
   - **禁止**用雪花/全局自增当 `seq`；**禁止**用客户端时间排序。
2. **`seq` 必须在事务内、与会话行锁一起分配**
   - `SELECT ... FOR UPDATE`（MySQL）→ `last_seq + 1` → 插入消息，同一事务。
   - 事务外分配（Redis `INCR`、号段）会产生永久空洞；v1 不做号段批发。
   - 允许事务回滚产生空洞，但客户端必须能处理“该 seq 不存在”。
3. **幂等靠 `client_msg_id`**
   - 唯一索引 `(conversation_id, sender_id, client_msg_id)`。
   - 幂等检查必须在发号之前；重发返回**首次分配的 seq**，不重新发号。
   - 并发撞唯一键时回滚整个事务再返回旧消息；**不要提交已经加过的 `last_seq` / `sent_seq`**。
   - `client_msg_id` 视为不可变：同一个 key 带不同 body 目前会静默返回旧消息；如果要严格化，返回 409，不要覆盖旧消息。
4. **历史分页只用游标**
   - `WHERE conversation_id=? AND seq<? ORDER BY seq DESC LIMIT ?`（历史）
   - `WHERE conversation_id=? AND seq>? ORDER BY seq ASC LIMIT ?`（补洞）
   - **禁止 OFFSET**：深分页越翻越慢，且插入会导致重复/跳过。
5. **已读只增不减**
   - 用 `WHERE read_seq < ?` 条件更新（等价 `GREATEST`，且 MySQL/SQLite 都支持）。
   - 乱序上报（先 100 再 90）必须保持 100。
   - 已读变更只 bump 自己的 `participant.version`；v1 不广播多端。
6. **未读数不能用 `max_seq - read_seq`**
   - 公式：`(last_countable_seq - read_countable_seq) - (sent_seq - read_sender_seq)`。
   - 系统消息不计未读；自己发的消息不计未读。
   - 水位只增不减，任一刻可由 `countable_seq` 重算（可自愈）。
7. **先落库再返回（ACK）**
   - 禁止先 ACK 后写库；发送成功 = 事务已提交。
8. **越权与可见范围**
   - 非成员 / 会话不存在统一 403。
   - `visible_from_seq` 是**权限**不是展示：开放群聊/入群历史前，必须在查询里按 `seq >= visible_from_seq` 裁剪。
9. **契约与响应**
   - 会话列表只返回摘要（最后一条 + 未读 + 版本），不返回完整历史。
   - `PullMessages` 的 `max_seq` 必须 ≥ 返回列表的最后一条 seq（避免客户端误判漏消息）。
   - 系统消息不对客户端开放（service 拒收）；消息表虽可容纳 system 类型，但 v1 没有生产入口。

**当前已实现 vs 未实现（不要把未实现当成已实现）：**

| 已实现 | 未实现 |
|---|---|
| 单聊自动建会话、发消息、幂等、连续 seq | 建群 / 加群 / 成员角色 |
| 拉历史 / 补洞（游标） | WebSocket / gateway / 在线推送 |
| 未读数（双水位） | MQ / outbox / 群扇出 |
| 已读推进、单聊对端已读位 | 撤回 / 编辑 / 删除 / 表情回应 |
| 非成员 403 | 消息搜索 / 内容审核 / E2EE |
| 注册 / 登录 / 当前用户 | 找回密码 / 第三方登录 / 刷新令牌 |
| 账号禁用（字段 + 校验） | 角色权限 / 组织架构 / 审计 |

---

## 7. 存储、事务与迁移

### 7.1 数据库

- **MySQL 是生产主库；SQLite 只用于测试/单机。**
- 表结构两份等价 DDL：`001_init_mysql.sql` + `001_init_sqlite.sql`。**改表必须两份一起改**。
- 生产代码禁止 `AutoMigrate`；迁移是编号 SQL，`make migrate` 按文件名排序执行 MySQL 版（跳过 `*_sqlite.sql`）。SQLite DDL 由测试自行加载。
- PO 模型**显式写字段 + `TableName()`**，不嵌入 `gorm.Model`，不依赖 GORM 的复数化/自动时间戳约定。
- 常用唯一索引就是正确性闸门：`(conversation_id, seq)`、`(conversation_id, sender_id, client_msg_id)`、`uk_username`。不要只在代码里判断“应该不会重复”。

### 7.2 数据层 SQL 必须跨库（MySQL + SQLite）

数据层只用标准 SQL。唯一的方言分支是加行锁：

```go
// MySQL 生成 FOR UPDATE；SQLite 原样（SQLite 没有行锁，测试用单连接串行化）
func rowLock(db *gorm.DB) *gorm.DB {
    if db.Dialector.Name() == "mysql" {
        return db.Clauses(clause.Locking{Strength: "UPDATE"})
    }
    return db
}
```

禁止直接写：

- `SELECT ... FOR UPDATE`（SQLite 不支持）
- `ON DUPLICATE KEY UPDATE`（MySQL 专属；用 GORM `clause.OnConflict` 或先查后插）
- `GREATEST(...)`（SQLite 不支持；用 `WHERE read_seq < ?` 条件更新）
- 解析驱动错误码（MySQL 1062 / SQLite `SQLITE_CONSTRAINT` 不一致）→ **插入失败后回查**，查到同名/同键就翻译成领域错误。

### 7.3 MySQL REPEATABLE READ 的快照坑（真实踩过）

MySQL 默认 RR：事务里**第一条普通 SELECT 会固定快照**。如果先普通 SELECT（没读到，因为对方事务未提交），再去抢行锁，拿到锁之后所有普通 SELECT 仍读旧快照：

- 并发首次发消息 → 双方都看不到对方刚提交的会话/成员 → 重复发号或误判非成员。

正确顺序：

1. **先写后当前读**：先幂等 INSERT，再 `SELECT ... FOR UPDATE` 回读；
2. 或**先加锁，再让第一条普通 SELECT 发生在锁之后**。

一句话：**不要在拿锁之前做普通 SELECT，然后把结果当真。**

### 7.4 事务

- `biz`：`tm.WithTx(ctx, func(txCtx context.Context) error { ... })` 控制边界。
- `data`：一律 `tx.WithContext(ctx, db)`，自动复用 ctx 里的事务；**不要**直接 `r.db.WithContext(ctx)`。
- `biz` 不出现 `*gorm.DB` / `*sql.Tx`；`*sql.Tx` 是 `pkg/tx` 的内部实现细节。
- 不要跨 Repository 共享同一个 `*gorm.DB` 链式对象（Statement 状态会残留）；`pkg/tx` 每次创建独立 Session 就是为此。
- 嵌套事务由 `pkg/tx` 用 SAVEPOINT 处理；不要在业务里自己开嵌套事务。

### 7.5 时间与 ID

- 写库时间统一 `time.Now().Truncate(time.Millisecond)`：MySQL `DATETIME(3)` 与 SQLite 精度不同，不截断会导致测试/排序抖动。
- 用户/会话/消息内部主键用 `uint64`；对外消息 ID 用 UUIDv7 hex。
- 不要依赖数据库默认值生成业务时间；由 biz 生成后显式写入。

---

## 8. 可观测性

四条铁律：

1. **ctx 是一等公民**：日志 / 链路 / 指标关联全靠 ctx，丢 ctx 即断链。
2. **仪器构造期创建，调用期只记录**：tracer / meter / counter 并发安全，禁止热路径 `new`。
3. **命名有界**：span 名中文可读且不含变量；metric 名固定；label 只放有限枚举。
4. **热路径用 Warn 不用 Error**：只有真故障用 Error。

### Trace

- 关键路径贯穿 OTel trace；span 名用中文、不含变量。
- 出错必须 `span.RecordError(err)` + `span.SetStatus(codes.Error, ...)`，并 `defer span.End()`。
- 请求 ID 就是 TraceID：不要另起一套 `X-Request-ID` 生成逻辑。

### Metrics

- OTLP 主动推送；生产不暴露 `/metrics`（`pkg/metrics` 端点为空时零开销）。
- **耗时统一毫秒**：`metric.WithUnit("ms")`，`float64(time.Since(start).Milliseconds())`。
  - 坑：记秒会让所有亚秒值落进第一个桶，分位数完全失效。
- 命名：`{svc}_{object}_{action}_duration_ms`。
- label 只放 `result=ok/error`、`outcome=hit/miss` 这类有限枚举；**禁止** uid、tid、key、url、error message。
- 记录指标必须传请求 ctx，禁止 `context.Background()`。

### 日志

- 见第 5.3 节：biz/service 不记错误日志，统一出口；带 ctx；中文；不打印敏感信息。
- 自动插桩：新增 Redis / OSS 客户端必须走 `pkg/cache`、`pkg/oss` 的共享构造器；绕过会丢日志/追踪。

---

## 9. Wire、代码生成与新增服务

### 9.1 Wire

- 每层一个 `wire.NewSet(...)`：`data.ProviderSet` / `biz.ProviderSet` / `service.ProviderSet` / `server.ProviderSet`。
- 每个服务一个 injector：`app/<svc>/wire.go`；组合根：`cmd/server/wire.go`。
- **改完任何构造函数签名 / ProviderSet 后必须跑 `make wire`**，并把生成的 `wire_gen.go` 一起提交。
  - 坑：不跑 wire，编译用的是旧依赖图，运行时少注入一个依赖才发现。
- `Provide` 签名只能出现公开类型 + 共享基础设施；internal 类型一律不能出现在参数/返回值。
- 组合根只做编排：`cmd/server/wire_gen.go` 里不应 import 任何 `app/*/internal/*`。

### 9.2 生成物

- `*.pb.go`、`wire_gen.go` **必须入库**，克隆即可构建。
- 改 proto 后跑 `make api` / `make config`；提交生成物。
- `make verify` 会重新生成并检查漂移：**工作区必须干净**（先提交或 stash 无关改动），否则 drift 检查会失败在无关文件上。

### 9.3 新增一个服务

1. `api/<svc>/v1/<svc>.proto`（包名 `api.<svc>.<version>`，路径 `/v1/...`）→ `make api`
2. `app/<svc>/conf/conf.proto` + `config.go` → `make config`
3. `app/<svc>/internal/{data,biz,service,server}` 四层，各自 `provider.go`
4. `app/<svc>/provider.go`（公开面 + `ProviderSet`）与 `app/<svc>/wire.go`（injector）
5. `cmd/server/wire.go` 加一行 `<svc>.ProviderSet`；`cmd/server/modules.go` 加一个参数；`cmd/server/main.go` 加 `-<svc>-conf` 并 `Load`
6. `makefile` 的 `wire` 目标加上 `./app/<svc>`
7. `make wire` + `make verify`

### 9.4 拆分一个服务

```bash
mv app/foo /path/to/foo-service/
mv api/foo /path/to/foo-service/api/
```

新增 `app/foo/cmd/foo/main.go`（在 `app/foo` 树内，可直接 import internal）：加载自己的 conf + 建 server + 调 `foo.Provide`，约 30 行。跨服务调用改为 gRPC client（`data/client/` 实现同一接口，组合根换一行 `wire.Bind`）。

---

## 10. 测试与验收

- **完成定义**：`make verify` 全绿（gofmt + vet + `go test -race` + 生成物零漂移）。
  - `-race` 需要 cgo 与 C 编译器（gcc/clang）。
  - `make verify` 的 drift 检查基于 `git diff`，工作区必须干净。
- 单元测试不需要 MySQL / Redis / OTLP。
- HTTP 层测试用 `httptest` 跑真实 kratos server（含中间件、编码器、CORS、健康检查）。
- **存储语义用真实 SQLite 测，不要只用 fake repo**：
  - seq 连续性、唯一索引幂等、游标分页、行锁排队，这些性质只有数据库能保证。
  - fake repo 只能证明“假实现自己也对”，上线才炸。
- SQLite 测试：`SetMaxOpenConns(1)` 串行化写入，模拟 MySQL 行锁排队；测试自己加载 `*_sqlite.sql`。
- MySQL 真库测试：读 `RYM_TEST_MYSQL_DSN`，未设置则 `t.Skip`，**默认不连任何数据库**；不要把 DSN 指向生产库。
- 测试里 bcrypt 用 `bcrypt.MinCost` 加速；生产保持默认 10。
- IM 验收以文档 §16.5 六条为准：重发幂等、并发无空洞、翻页无重复、已读不乱序、未读正确、非成员 403。
- 架构测试是 CI 门禁：不要为了“先跑通”注释掉或放宽 `pkg/architecture`。

---

## 11. 常用命令

```bash
cp config.yaml.example config.yaml
JWT_SECRET=dev-secret make run          # 签发登录令牌需要 jwt_secret
make build                              # 或 make rebuild（config + api + wire + build）
make migrate DB_NAME=rym [DB_HOST=...]  # 按序执行 app/*/migrations/*.sql（跳过 *_sqlite.sql）
make verify                             # gofmt + vet + test -race + 生成物漂移检查
make api / config / wire                # 单独代码生成
make fmt / tidy                         # 格式化 / 依赖整理（静态检查用 go vet ./...，make verify 已包含）
```

- `config.yaml`、`bin/`、`data/`、`temp/` 已 gitignore，不要提交。
- SQLite 迁移不通过 make 执行，由测试加载；本机没有 `sqlite3` CLI 不影响测试。
- 业务配置：etcd key `rym/{env}/configs/<svc>/config.yaml`；未配置 etcd 时读
  `configs/<svc>/config.yaml`。目前只有 im 接入热更新。

---

## 12. 提交与协作

- Conventional Commits + 中文描述：`feat(im): ...`、`fix(im): ...`、`test(im): ...`、`docs: ...`、`chore: ...`。
- **每个逻辑单元单独 commit**，不要一个 commit 混三件事。
- commit body 写清：
  - 做了什么；
  - **简化了什么、代价是什么、将来怎么补**；
  - 如果修了坑，写清坑的触发条件（后来者才不会再踩）。
- 不要顺手重构、不要扩大范围、不要改无关文件。
- 改动契约 / 行为 / 配置项时同步更新 `README.md` 或 `docs/`。
- 提交前 `git diff` 自查；提交后 `git status` 确认没有漏文件。
- 需要 push 时推 `origin/master`；推送前确认 `make verify` 是绿的。
- 设计问题以 `docs/im-design-best-practices.md` 为准；文档没写的，先问，不要先做再说。

---

## 13. 踩坑自查清单（提交前逐条过）

| # | 坑 | 正确做法 |
|---|---|---|
| 1 | `os.Getenv` 读业务配置 | 进 conf/proto，由 Wire 注入 |
| 2 | biz import proto / data / gorm | biz 不依赖 proto/data/gorm；只依赖标准库、`pkg/errs`、`pkg/tx`、少量通用库（如 bcrypt）和自有 DTO |
| 3 | service 里写业务、循环调 biz | 每个 RPC 只调一次 biz |
| 4 | 服务之间互相 import | 只能经 `api/` 或 `pkg/` |
| 5 | `Provide` 签名出现 internal 类型 | 只暴露 `Module` + 公开基础设施 |
| 6 | 改了构造函数不跑 `make wire` | 生成物一起提交 |
| 7 | 生成物不入库 | `*.pb.go` / `wire_gen.go` 必须入库 |
| 8 | 用 `AutoMigrate` | 编号 SQL + `make migrate` |
| 9 | 热路径 `new` tracer/meter | 构造期创建，调用期只记录 |
| 10 | 日志/指标不带 ctx | `log.WithContext` / `log.FromContext`，指标传请求 ctx |
| 11 | 4xx 记 Error / biz 也记错误日志 | 只由 `response.EncodeError` 记 5xx |
| 12 | metric 用秒 | 统一毫秒 + 默认桶 |
| 13 | label 放 uid/tid/url | 只放有限枚举 |
| 14 | 密码/令牌/消息正文进日志 | 永不打印 |
| 15 | 登录错误区分“用户不存在/密码错” | 同一个 401 + dummy bcrypt |
| 16 | 非成员返回 404 | 统一 403，避免会话枚举 |
| 17 | 无条件信任 XFF | 仅 `trusted_proxies` 命中时采信 |
| 18 | CORS `*` + credentials | 白名单回显 + `Vary: Origin` |
| 19 | seq 事务外分配 | 会话行锁 + 同事务插入 |
| 20 | 用雪花当 seq | UUIDv7 管唯一，seq 管顺序 |
| 21 | 没有 `client_msg_id` 唯一键 | 重发会重复 |
| 22 | 先 ACK 后落库 | 提交后再返回 |
| 23 | 未读 `max_seq - read_seq` | 双水位公式 |
| 24 | `SET read_seq = ?` | `WHERE read_seq < ?` 条件更新 |
| 25 | OFFSET 翻历史 | `seq <` / `seq >` 游标 |
| 26 | MySQL 先普通 SELECT 再抢锁 | 先写后当前读 / 先锁后读 |
| 27 | 数据层写 `FOR UPDATE` / `ON DUPLICATE KEY` / `GREATEST` | 标准 SQL + `data.rowLock` 唯一分支 |
| 28 | 解析 1062 / SQLITE_CONSTRAINT | 插入失败后回查翻译 |
| 29 | 时间不截断 | `Truncate(time.Millisecond)` |
| 30 | 用户名不归一化 | 统一小写，跨库一致 |
| 31 | 全局 `require_auth=true` 拦住注册/登录 | 单体保持 false，handler 自查 401 |
| 32 | `jwt_secret` 为空还能启动 | 签发服务启动即失败 |
| 33 | 用 fake repo 验 DB 语义 | 真实 SQLite + 可选 MySQL |
| 34 | `make verify` 时工作区有无关改动 | 先提交/stash |
| 35 | 忘了 `visible_from_seq` | 开放群聊前必须按 seq 裁剪 |
| 36 | etcd key 漏了 env / 用前缀扫描 | 精确读 `<prefix>/<env>/configs/<svc>/config.yaml` |
| 37 | 把 `Settings` 按值存进 Usecase | 注入 `SettingsProvider`，每次用例入口读一次 |
| 38 | 动态配置里放 DB/监听/密钥 | 只放纯业务值；资源类配置启动时读一次 |

---

**最后一条：不确定就先停下来问。** 这个仓库更怕“做错后再改”，而不是“少做一个功能”。

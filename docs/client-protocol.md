# 客户端协议说明（Wails 桌面 / Flutter 移动共用）

本文件是客户端实现的**行为契约**，不是服务端 API 文档。
服务端接口以 `api/**/*.proto` 为准；本文只规定“客户端怎么用这些接口”。

## 1. 本地状态模型

每个会话保存：

| 字段 | 说明 |
|---|---|
| `conversation_id` | 服务端会话 id |
| `peer_id` | 单聊对端 user_id（群聊为 0） |
| `last_seq` | 本地已连续收到的最大 seq（**不是 max_seq**） |
| `read_seq` | 已上报的已读位 |
| `unread` | 本地未读数（可用服务端返回校准） |

每条消息保存：

| 字段 | 说明 |
|---|---|
| `msg_id` | 服务端唯一 id（成功后才有） |
| `client_msg_id` | 客户端生成，重试必须复用 |
| `seq` | 服务端会话内序号（成功后才有） |
| `status` | `sending` / `sent` / `failed` |
| `body` | JSON 字符串（文本为 `{"text":"..."}`） |

## 2. 首次登录

1. `POST /v1/users/login` → 保存 `access_token`（桌面放系统 keychain，移动放安全存储）。
2. `GET /v1/users/me` → 当前用户。
3. `GET /v1/im/conversations` → 会话列表（含 `peer_id`、`last_seq`、`unread_count`、`peer_read_seq`）。
4. `POST /v1/users/batch` → 批量取 `peer_id` 的昵称。
5. 打开会话：`GET /v1/im/messages?conversation_id=X&limit=50` → 按 `seq` 升序插入；`last_seq = 最后一条 seq`。

## 3. 发送消息

1. 本地生成 `client_msg_id`（UUIDv4/v7），先插入 `status=sending` 的本地消息。
2. `POST /v1/im/messages`，`to_user_id` 或 `conversation_id` 二选一。
3. 成功：
   - 用响应里的 `message.msg_id` / `seq` / `created_at` 替换本地临时消息；
   - `status=sent`；
   - 若响应 `duplicated=true`，用返回的旧消息按 `msg_id` 去重。
4. 网络失败：保持 `sending`，指数退避重试，**复用同一个 `client_msg_id`**。
5. 4xx（参数/权限）：标记 `failed`，展示原因，不自动重试。

## 4. 实时事件（WS）

连接：

- 桌面（Wails Go 后端）/ Flutter IO：`Authorization: Bearer <JWT>`；
- 浏览器 / Flutter Web：先 `POST /v1/im/ws-ticket` 拿 30s 单次 ticket，
  再连 `ws://host:8001/v1/im/ws?ticket=...`。

事件：

```json
{"v":1,"type":"hello","data":{"conn_id":"...","heartbeat_sec":25,"max_lifetime_sec":86400}}
{"v":1,"type":"message.created","data":{"conversation_id":1,"message":{"msg_id":"...","seq":42,"sender_id":1,"type":1,"body":"...","created_at":"..."}}}
{"v":1,"type":"read.updated","data":{"conversation_id":1,"user_id":2,"read_seq":41}}
```

处理规则：

- `message.created`：
  - `seq == last_seq + 1` → 追加，`last_seq++`；
  - `seq > last_seq + 1` → 标记该会话需要补洞；
  - `seq <= last_seq` → 按 `msg_id` 去重后丢弃。
- `read.updated`：按 `user_id` 更新对应会话的 peer 已读位。
- 未知 `type`：忽略（协议向前兼容）。

## 5. 补洞

发现跳号（WS 跳号、重连、应用回到前台）：

```
GET /v1/im/messages?conversation_id=X&after_seq=<last_seq>&limit=100
```

- 返回按 `seq` 升序；
- 追加到 `last_seq`，直到 `has_more=false`；
- 若服务端明确没有某个 seq（历史空洞），跳过该号，不要无限重试。

## 6. 已读

- 只有聊天页可见且消息已渲染时上报；
- `POST /v1/im/conversations/{id}/read`，`read_seq = 当前可见最大 seq`；
- 防抖 300–500ms；只增不减，重复/乱序上报安全；
- 不要因为用户滚动到顶部就回拨 `read_seq`。

## 7. 重连

- 指数退避：500ms/1s/2s/…，上限 30s，**full jitter**；
- 连接稳定 30s 后重置退避计数；
- 每次重连都重新拿 ticket（浏览器）或复用 JWT（桌面/移动原生）；
- 重连成功后的顺序：
  1. `GET /v1/im/conversations`（刷新列表/未读）；
  2. 当前打开会话 `after_seq` 补洞；
  3. 其他标记为脏的会话在打开时补洞。

## 8. 本地缓存

- 桌面：SQLite（建议 `modernc.org/sqlite` 纯 Go，避免 cgo）；
- 移动：drift / sqflite；
- 至少持久化：会话、消息、`last_seq`、`read_seq`、outbox；
- 退出登录时清空 token 与本地消息（隐私）。

## 9. 错误与登录态

- 401：token 失效 → 清 token，回登录页；
- 403：非成员/无权限 → 不要重试；
- 404：用户/会话不存在 → 提示并刷新列表；
- 429/5xx：指数退避重试，展示“网络不稳定”状态。

## 10. 不做（v1 客户端）

- 端到端加密；
- 群聊建群/成员管理（等后端提供）；
- 消息撤回/编辑；
- 离线推送（移动端后续接 APNs/FCM）；
- presence / 正在输入。

# rym

**IM 服务端**：单体部署、服务可拆分。目录与依赖形状对齐生产微服务仓库，
当前只跑一个进程。未来拆服务 = `mv app/<svc>` + 补一个 30 行 `main.go`，
不需要改 internal 里任何代码。

> 本仓库由 [`CycleZero/gin-template`](https://github.com/CycleZero/gin-template)
> 脚手架初始化。传输层是 kratos `transport/http`（路由由 proto 生成）。
> `app/demo` 是脚手架自带的示例模块，保留作为四层写法的活样板
> （`app/im` 是第一个真实业务模块，实现参考它即可）。

## 技术栈

| 能力 | 选型 |
|---|---|
| HTTP | `github.com/go-kratos/kratos/v3/transport/http`（路由由 proto 生成） |
| 契约 | protobuf + `google.api.http`（buf 生成，生成物入库） |
| 依赖注入 | Google Wire（每个服务一个 injector + 一个组合根） |
| 数据 | GORM / MySQL，事务通过 `pkg/tx`（context 传递 `*sql.Tx`） |
| 可观测性 | OTel（trace / metrics / logs 三路 OTLP）、slog + zap |
| 配置 | kratos config（yaml + `APP_` 环境变量占位符） |

## 目录结构

```
api/<svc>/v1/            对外契约（proto + 生成物），服务之间只认它
                         proto 包名固定为 api.<svc>.<version>（如 api.demo.v1）
conf/common.proto        进程级共享配置（Server/Log/Otel/Data/Security）
app/<svc>/
├── provider.go          公开装配面：Module + ProviderSet（cmd 只能看到这里）
├── wire.go              //go:build wireinject，服务自己的 injector
├── wire_gen.go          生成物（与 internal 同树，可合法引用 internal）
├── conf/                服务自有配置（拆分时整目录搬走）
└── internal/
    ├── data/            PO + 仓储实现（model/repo/convert/provider）
    ├── biz/             领域模型 + 仓储接口 + 用例 + 事务边界
    ├── service/         proto ↔ biz DTO（薄适配）
    └── server/          把生成的路由挂到共享 http.Server
cmd/server/              单体阶段唯一入口 + 组合根
pkg/
├── transport/           kratos server、中间件链、响应编码器、pprof、健康检查
├── db/                  参数化的 MySQL/GORM 连接 + 健康探测
├── architecture/        AST 架构门禁测试
└── cache/errs/log/metrics/oss/otelx/response/trace/tx/module

Redis 连接与客户端管理在 pkg/cache（`cache.NewRedisClient`），
GORM 的 slog 适配在 pkg/db，`log.FromContext` 提供带链路字段的默认 logger。
```

## 分层与依赖规则

```
cmd/server      只编排：基础设施 + 各服务公开面 + khttp server
    ↓ wire
app/<svc>/internal/server     协议装配（只认识 service）
    ↓
app/<svc>/internal/service    proto ↔ biz（不 import data）
    ↓
app/<svc>/internal/biz        领域模型 + Repo 接口 + 用例（零 proto、零 gorm）
    ↑ 实现                ↓ 调用
app/<svc>/internal/data       Repo 实现 + PO + 事务
```

硬规则（`pkg/architecture` 里用 AST 测试锁死）：

| 规则 | 测试 |
|---|---|
| `biz` 不 import `api/`、不 import `data` | `TestBizDoesNotImportProtoOrData` |
| `service` 不 import `data` | `TestServiceDoesNotImportData` |
| 生产代码禁止 `AutoMigrate` | `TestNoAutoMigrate` |
| 服务之间零 import（只能走 `api/` 或 `pkg/`） | `TestModuleIsolation` |

另外的约定：

- `biz` 不出现 `*gorm.DB` / `*sql.Tx`，事务边界用 `tx.TxManager.WithTx`；
- `data` 统一用 `tx.WithContext(ctx, db)`，自动复用 ctx 里的事务；
- **错误日志单一出口**：`biz`/`service` 不记错误日志，统一由 `pkg/response.EncodeError`
  输出（只有 5xx 记 Error，4xx 不写日志）——两处都记会产生重复日志，且 404 会被记成
  Error 级别、制造告警噪声；
- 无包级可变全局（中间件顺序写死在 `pkg/transport.NewMiddlewares`）；
- 业务配置一律进 proto，禁止用 `os.Getenv` 读业务配置。

## Wire 链（单体阶段）

Wire 的 injector 必须和 `internal` 在同一棵树里，因为 `wire_gen.go` 会按字面量
引用被注入的类型。因此采用**两段式**：

**1）每个服务一个 injector（位于 `app/<svc>/` 根，可合法 import internal）**

```go
// app/demo/wire.go
func Provide(demoCfg *conf.Config, srv *khttp.Server, db *gorm.DB,
    tm tx.TxManager, logger *slog.Logger) (*Module, error) {
    panic(wire.Build(data.ProviderSet, biz.ProviderSet,
        service.ProviderSet, server.ProviderSet, NewSettings, newModule))
}
```

对外只暴露公开类型（`Module`），internal 类型一个都不许出现在签名里——
否则组合根无法引用（Go internal 规则）。`newModule` 负责把
`internal/server.Module` 收敛成公开的 `*demo.Module`。

**2）组合根只做编排**

```go
// cmd/server/wire.go
func initApp(bc *conf.Bootstrap, demoCfg *democonf.Config, logger *slog.Logger) (*kratos.App, func(), error) {
    panic(wire.Build(
        NewApp,
        newDBParams, newRedisParams,   // conf → 原始参数（pkg 不依赖配置类型）
        newReadinessChecker,           // /readyz：db.Ping + cache.Ping
        db.ProviderSet,                // *gorm.DB + 关闭函数
        cache.ProviderSet,             // *redis.Client + 关闭函数
        tx.NewTxManager,
        transport.ProviderSet,         // *khttp.Server / pprof / 中间件
        demo.ProviderSet,              // ← 新增服务加一行
        newModuleSet,                  // ← modules.go 加一个参数
    ))
}
```

生成的 `cmd/server/wire_gen.go` 里**只出现公开包**（`app/demo`、`pkg/*`），
不会 import 任何 `app/*/internal/*`。这是"可拆分"的结构保证，也是本方案
与原 `ServiceHub`（上帝对象）+ 包级中间件注册表的根本区别。

中间件与生命周期：

- 中间件顺序只在 `pkg/transport.NewMiddlewares` 写一次；
- kratos 负责信号处理与停机超时，`main` 负责资源/遥测收尾；
- 注册顺序由依赖边（`Module → *khttp.Server`）保证，不再需要
  `panic("中间件注册未完成")`。

## 快速开始

```bash
cp config.yaml.example config.yaml      # 按需改 DB/Redis
make build                              # 或 make rebuild（含代码生成）
make migrate DB_NAME=rym                # 建表（编号 SQL，非 AutoMigrate）
JWT_SECRET=dev-secret ./bin/rym -conf config.yaml
```

> `JWT_SECRET` 是登录令牌的签名密钥（`security.jwt_secret`）。
> 用户服务在它为空时**启动失败**，避免带病运行；本地随便给一个开发值即可。

常用接口：

```bash
curl localhost:8000/healthz            # 存活探针
curl localhost:8000/readyz             # 就绪探针（MySQL/Redis）
curl localhost:8000/v1/demos           # demo 列表
curl -X POST localhost:8000/v1/demos \
  -H 'Content-Type: application/json' \
  -d '{"name":"demo","description":"hello"}'
```

## 用户模块（v1）

注册 / 登录 / 当前用户 / **用户发现**。令牌是 HS256 JWT，
后续请求用 `Authorization: Bearer <token>`（由 `pkg/transport` 统一校验）。

| RPC | HTTP | 说明 |
|---|---|---|
| `Register` | `POST /v1/users/register` | 用户名统一转小写；密码 bcrypt 存储，永不下发 |
| `Login` | `POST /v1/users/login` | 返回 `access_token` / `expires_at` / `user` |
| `GetMe` | `GET /v1/users/me` | 校验令牌与账号状态 |
| `SearchUsers` | `GET /v1/users/search` | username / nickname **前缀**搜索，需要登录，最多 50 条 |
| `GetUser` | `GET /v1/users/{id}` | 公开资料；不存在/禁用统一 404 |
| `BatchGetUsers` | `POST /v1/users/batch` | 批量取资料（最多 100 个 id），用于会话列表/聊天头部 |

不可回退的约束：

- 密码用 bcrypt（生产 cost=10），限制 8-72 字节；明文/哈希都不进日志、不进响应；
- 用户名统一小写后存储，避免 MySQL 大小写不敏感、SQLite 大小写敏感的跨库差异；
- 用户不存在与密码错误返回**同一个 401**，且未知用户也做一次 dummy bcrypt，
  避免通过错误信息或响应时间枚举账号；
- 账号禁用只在密码校验通过后提示；
- `security.require_auth` 是**全局**开关，打开会连注册/登录一起拦。单体阶段保持
  `false`，受保护接口由 handler 自己检查 `UserID == 0` 返回 401。

```bash
# 注册
curl -s localhost:8000/v1/users/register -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"password123","nickname":"Alice"}'

# 登录（把 access_token 存到变量）
TOKEN=$(curl -s localhost:8000/v1/users/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"password123"}' | jq -r '.data.access_token')

# 当前用户
curl -s localhost:8000/v1/users/me -H "Authorization: Bearer $TOKEN"
```

## IM 模块（v1）

单聊 + ≤200 人小群，只做四件事：**发消息 / 拉历史 / 未读数 / 已读**。
设计依据与取舍见 `docs/im-design-best-practices.md`，落地决策在 §16。

| RPC | HTTP | 说明 |
|---|---|---|
| `SendMessage` | `POST /v1/im/messages` | `conversation_id=0` 时给 `to_user_id`，按双方 uid 派生单聊会话 |
| `PullMessages` | `GET /v1/im/messages` | 游标分页：`before_seq` 翻历史 / `after_seq` 补洞，禁止 OFFSET |
| `ListConversations` | `GET /v1/im/conversations` | 摘要 + 未读数 + 单聊对端已读位 + `peer_id` |
| `OpenSingleConversation` | `POST /v1/im/conversations` | 幂等打开/创建单聊，返回会话（不产生消息） |
| `MarkRead` | `POST /v1/im/conversations/{id}/read` | `read_seq` 只增不减，乱序上报安全 |
| `CreateWSTicket` | `POST /v1/im/ws-ticket` | 签发 30 秒单次 WS 票据（浏览器/Flutter Web 用） |

不可回退的约束：

- `msg_id`（UUIDv7）管唯一，`seq`（会话内连续）管顺序，两者不混用；
- `seq` 由 `conversations.last_seq` 在同一事务里加行锁分配（分配序 == 提交序）；
- `(conversation_id, sender_id, client_msg_id)` 唯一键做幂等，重发返回首次分配的 `seq`；
- 未读 = `(last_countable_seq - read_countable_seq) - (sent_seq - read_sender_seq)`，
  可由水位重算，不用 `max_seq - read_seq`；
- 非成员与不存在的会话统一 403，不泄漏会话是否存在。

### 本地跑一遍闭环

IM 接口需要已鉴权用户。先注册两个用户、拿到令牌，再用 `Authorization: Bearer`：

```bash
cp config.yaml.example config.yaml
cp configs/im/config.yaml.example configs/im/config.yaml
cp configs/user/config.yaml.example configs/user/config.yaml
JWT_SECRET=dev-secret ./bin/rym -conf config.yaml -im-conf configs/im/config.yaml -user-conf configs/user/config.yaml

# 注册 alice / bob（响应里的 data.id 就是 user_id）
curl -s localhost:8000/v1/users/register -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"password123"}'
curl -s localhost:8000/v1/users/register -H 'Content-Type: application/json' \
  -d '{"username":"bob","password":"password123"}'

A=$(curl -s localhost:8000/v1/users/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"password123"}' | jq -r '.data.access_token')
B=$(curl -s localhost:8000/v1/users/login -H 'Content-Type: application/json' \
  -d '{"username":"bob","password":"password123"}' | jq -r '.data.access_token')

# alice(1) 给 bob(2) 发消息；响应里的 data.message.conversation_id 是会话 id
CONV=$(curl -s localhost:8000/v1/im/messages -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $A" \
  -d '{"to_user_id":2,"client_msg_id":"c-1","type":1,"body":"{\"text\":\"hello\"}"}' \
  | jq -r '.data.message.conversation_id')

# bob 拉历史
curl -s "localhost:8000/v1/im/messages?conversation_id=$CONV" -H "Authorization: Bearer $B"

# bob 标记已读
curl -s -X POST "localhost:8000/v1/im/conversations/$CONV/read" \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $B" -d '{"read_seq":1}'

# alice 看会话列表：data.list[0].peer_read_seq == 1 即闭环
curl -s localhost:8000/v1/im/conversations -H "Authorization: Bearer $A"
```

> 网关模式下也可以不签发本地令牌：设置 `APP_TRUST_GATEWAY_IDENTITY=true`，
> 改发 `x-md-global-auth-user-id: <uid>`。这只用于本地调试，生产由网关负责。

### 配置来源：etcd（可选，im 已接入）

- key：`rym/{env}/configs/im/config.yaml`（`prefix` / `env` 在 `config.yaml` 的
  `etcd` 段配置，默认 `rym` / `dev`）。
- `etcd.endpoints` 为空：只读本地 `configs/im/config.yaml`，改完重启生效。
- 配置了 etcd：启动时读取该 key 并 watch；一次 PUT = 一次原子更新，不做字段级合并。
  启动时 key 缺失 / 解码 / 校验失败都会直接失败（fail-closed）。
- 运行期非法更新或删除 key：保留上一版，打日志等下一次变更。
- 目前只有 im 的 Settings 每请求读快照；demo / user 仍启动时读本地配置。

```yaml
# config.yaml
etcd:
  endpoints: ["127.0.0.1:2379"]
  prefix: "rym"
  env: "dev"
  dial_timeout_seconds: 3
```

### WebSocket 实时推送

发消息仍然走 HTTP；WS 只做**服务端推送**（新消息、已读位变化），
这样发送的幂等/seq/事务语义不重复实现，断线丢帧也不影响正确性。

- 监听：`server.ws`（独立端口，默认 `0.0.0.0:8001`，路径 `/v1/im/ws`）。
- 鉴权双模：
  - Wails Go 后端 / Flutter IO：WS 握手直接带 `Authorization: Bearer <JWT>`；
  - 浏览器 / Flutter Web：先 `POST /v1/im/ws-ticket` 拿 30 秒单次票据，
    再连 `ws://.../v1/im/ws?ticket=...`（Redis `GETDEL` 兑换，只能用一次）。
- 票据示例：

```bash
TICKET=$(curl -s -X POST localhost:8000/v1/im/ws-ticket \
  -H "Authorization: Bearer $TOKEN" | jq -r '.data.ticket')
# 客户端连 ws://host:8001/v1/im/ws?ticket=$TICKET
```

- 事件：
  - `hello`：`conn_id` / `heartbeat_sec` / `max_lifetime_sec`
  - `message.created`：完整消息（客户端发现 seq 跳号仍然走 `after_seq` 补洞）
  - `read.updated`：`conversation_id` / `user_id` / `read_seq`
- 语义：
  - 事务提交后才推送；推送失败只影响实时性，消息以 DB 为准；
  - 每连接 64 条发送队列，慢客户端直接断开（4008），重连后补洞；
  - 25s 心跳、60s 内无 pong 判死；单用户最多 5 连接；连接最长 24h；
  - 单体阶段是进程内连接表；多实例需要 Redis Pub/Sub（未做）；
  - 反向代理必须 `proxy_read_timeout > 心跳间隔`（nginx 默认 60s，建议 ≥300s）。

```yaml
server:
  ws:
    enable: true
    host: "0.0.0.0"
    port: 8001
    heartbeat_seconds: 25
    pong_timeout_seconds: 10
    max_connections_per_user: 5
    max_lifetime_seconds: 86400
```

### 存储：MySQL 主库，SQLite 仅测试

- 表结构两份等价 DDL：`app/im/migrations/001_init_mysql.sql`（生产）与
  `001_init_sqlite.sql`（测试/单机）；改表必须两份一起改。
- 迁移由 `schema_migrations` 记录（`make migrate` 不会重复执行）；新增变更用
  `002_*.sql` 增量迁移，不要改已发布的 001。
- 数据层不用 `FOR UPDATE` / `ON DUPLICATE KEY` / `GREATEST` 等方言专用 SQL，
  唯一分支是 MySQL 的 `SELECT ... FOR UPDATE`（`data.rowLock`）。
- 测试默认跑 SQLite（`go test ./app/im/...`，无需任何外部服务）；
  MySQL 真库验收用：

```bash
RYM_TEST_MYSQL_DSN='root:pass@tcp(127.0.0.1:3306)/rym_test?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./app/im/internal/data -run TestMySQLAcceptance -v
```

## 客户端（已定）

- **桌面：Wails v3，始终 latest。** Go 后端负责 HTTP/WS、本地缓存、重连、outbox、托盘与通知；
  前端用 React/TS，通过 Wails bindings/events 调用。
- **移动：Flutter 最新 stable。**
- 客户端独立仓库：`rym-desktop` / `rym-mobile`；本仓库只维护 `api/` 契约与 HTTP/WS 协议。
- WS 鉴权双模：Wails / Flutter IO 用 `Authorization` 头；浏览器 / Flutter Web 用 30s 单次 ticket。
- 选型理由、代价与版本策略：`docs/adr/0001-client-stack.md`。

## 响应契约

成功（业务数据统一在 `data` 下）：

```json
{"code":0,"message":"ok","data":{"id":1,"name":"demo"},"trace_id":"..."}
```

失败（4xx/5xx 由 `errs.Resolve` 决定，细节只进日志）：

```json
{"code":40400,"message":"记录不存在","trace_id":"..."}
```

`trace_id` ≡ 响应头 `X-Request-ID` ≡ 日志 `trace.id` ≡ 链路 TraceID，
上游传 `traceparent` 时会续接，不新起链路。

> 终态接入网关后，信封由网关统一包装，服务可直接返回裸 proto；
> 单体阶段由 `pkg/response` 兜底，客户端契约不变。

## 配置

- **进程级**：`conf/common.proto`（server/log/otel/data/security/etcd/ws）。
- **服务级**：`app/<svc>/conf/conf.proto`，文件默认 `configs/<svc>/config.yaml`（示例见
  `configs/{demo,im,user}/config.yaml.example`，分别用 `-demo-conf` / `-im-conf` /
  `-user-conf` 指定路径），缺失时用默认值。
- 环境变量只用于**部署覆盖**：`config.yaml` 里写成 `${KEY:default}` 的字段才能被
  `APP_KEY` 覆盖；禁止用 `os.Getenv` 读业务配置。
- 安全配置 fail-closed：`security.require_auth=true` 且没有 `jwt_secret` 时启动失败；
  用户服务签发登录令牌也要求 `jwt_secret` 非空，否则启动失败。
- `server.http.trusted_proxies`：可信反向代理 CIDR 白名单。只有 TCP 对端命中它，
  才采信 `X-Real-IP` / `X-Forwarded-For`；留空（默认）表示谁都不信，直连场景的正确取值。

## 可观测性

- trace：始终安装 Provider（无 OTLP 端点时 NeverSample + 无导出器），保证请求 ID 闭环；
- metrics：端点为空时完全不初始化（零开销）；
- logs：控制台 + 文件轮转 + OTLP，自动带 `trace.id` / `span.id`；
- pprof：只监听独立端口，`enable=true` 但 `port=0` 时直接跳过（绝不挂业务端口）。

## 测试与验收

```bash
make verify     # gofmt + go vet + go test -race + 生成物漂移检查
```

> `-race` 需要 cgo 与 C 编译器（gcc/clang）；没有时先用 `go test ./...` 验证。

单元测试不需要 MySQL / Redis / OTLP；HTTP 层测试用 `httptest` 直接跑真实的
kratos server（含中间件、编码器、CORS、健康检查）。

代码生成（生成物全部入库，克隆即可构建）：

```bash
make api      # api/<svc>/v1/*.proto → *.pb.go / *_grpc.pb.go / *_http.pb.go
make config   # conf/ + app/<svc>/conf/ 的 proto → *.pb.go
make wire     # 各服务 injector + cmd/server 组合根
```

## 新增一个服务

1. `api/foo/v1/foo.proto`（`option (google.api.http)`，路径 `/v1/...`）→ `make api`
2. `app/foo/conf/conf.proto` → `make config`
3. `app/foo/internal/{data,biz,service,server}` 四层，各自 `provider.go`
4. `app/foo/provider.go`（公开面 + `ProviderSet`）与 `app/foo/wire.go`（injector）
5. `cmd/server/wire.go` 加一行 `foo.ProviderSet`；`cmd/server/modules.go` 加一个参数
6. `make wire`

## 拆分一个服务

```bash
mv app/foo /path/to/foo-service/
mv api/foo /path/to/foo-service/api/
```

新增 `app/foo/cmd/foo/main.go`（在 `app/foo` 树内，可直接 import `internal`）：
加载自己的 conf + 建 server + 调 `foo.Provide`，约 30 行。跨服务调用改为
gRPC client（`data/client/` 实现同一接口，组合根换一行 `wire.Bind`）。

## 已知限制 / 下一步

- IM v1 未接网关：CORS/JWT/信封仍由本服务兜底（已留双模：`trust_gateway_identity`）；
- 未引入 etcd / 服务发现 / DTM（单库 + 单进程阶段不需要）；
- 事件/outbox/MQ 尚未落地（`pkg/tx` 已就绪，outbox 在 P2 阶段接入）；
- WS 实时推送只支持单进程：多实例需要 Redis Pub/Sub / 网关路由；
- WS 没有 presence / 正在输入 / 离线推送（APNs/FCM）；
- 桌面 / 移动客户端尚未创建（Wails / Flutter）；
- IM 群聊只有存储模型，没有建群/成员管理 RPC（留给组织/群模块）；
- IM 撤回/编辑/搜索未做，消息表已预留 `status` / `version` / `edited_at` / `body_text`；
- 用户模块没有找回密码、第三方登录、刷新令牌、角色权限；
- 用户模块登录没有限流/验证码，账号枚举只做了错误信息与耗时对齐；
- `app/demo` 的分页仍是 offset；IM 消息历史已用游标（`seq < ?`）。

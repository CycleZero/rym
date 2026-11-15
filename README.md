# gin-template

**单体部署、服务可拆分**的 Go 服务端骨架：目录与依赖形状对齐生产微服务仓库，
但当前只跑一个进程。未来拆服务 = `mv app/<svc>` + 补一个 30 行 `main.go`，
不需要改 internal 里任何代码。

> 注意：`gin` 已完全移除（传输层换成 kratos `transport/http`），仓库名保留是历史原因。

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
make migrate DB_NAME=gin_template       # 建表（编号 SQL，非 AutoMigrate）
./bin/server -conf config.yaml
```

常用接口：

```bash
curl localhost:8000/healthz            # 存活探针
curl localhost:8000/readyz             # 就绪探针（MySQL/Redis）
curl localhost:8000/v1/demos           # 列表
curl -X POST localhost:8000/v1/demos \
  -H 'Content-Type: application/json' \
  -d '{"name":"demo","description":"hello"}'
```

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

- **进程级**：`conf/common.proto`（server/log/otel/data/security）。
- **服务级**：`app/<svc>/conf/conf.proto`，文件默认 `configs/<svc>.yaml`（示例见
  `configs/demo.yaml.example`，用 `-demo-conf` 指定路径），缺失时用默认值。
- 环境变量只用于**部署覆盖**：`config.yaml` 里写成 `${KEY:default}` 的字段才能被
  `APP_KEY` 覆盖；禁止用 `os.Getenv` 读业务配置。
- 安全配置 fail-closed：`security.require_auth=true` 且没有 `jwt_secret` 时启动失败。
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

- 未接入 API 网关：CORS/JWT/信封仍由本服务兜底（已留双模：`trust_gateway_identity`）；
- 未引入 etcd / 服务发现 / DTM（单库 + 单进程阶段不需要）；
- 事件/outbox/MQ 尚未落地（`pkg/tx` 已就绪，outbox 在 P2 阶段接入）；
- 分页仍是 offset（IM 深分页需改游标分页 `WHERE seq < ? ORDER BY seq DESC LIMIT n`）。

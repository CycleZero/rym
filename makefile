BINARY_NAME := rym
BINARY_PATH := ./bin/${BINARY_NAME}
MAIN_PKG    := ./cmd/server

# 代码生成目标：
#   api    api/<svc>/v1/*.proto  → *.pb.go / *_grpc.pb.go / *_http.pb.go（入库）
#   config conf/*.proto + app/<svc>/conf/*.proto → *.pb.go（入库）
#   wire   每个服务一个 injector（app/<svc>）+ 组合根（cmd/server）
#
# 工具链（一次性安装，版本与 go.mod 对齐）：
#   go install github.com/bufbuild/buf/cmd/buf@latest
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#   go install github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v3@latest
#   或直接复用 vcyuan 参考仓库的 tools 安装脚本

.PHONY: api config wire build rebuild run tidy verify fmt lint migrate

# 对外契约代码生成
api:
	buf lint
	buf generate --template buf.gen.api.yaml

# 配置结构代码生成（进程级 + 每服务）
config:
	buf generate --template buf.gen.conf.yaml
	buf generate --template buf.gen.app.yaml

# Wire 依赖注入代码生成（生成的 wire_gen.go 必须入库）
wire:
	go run -mod=mod github.com/google/wire/cmd/wire ./app/demo ./app/im ./cmd/server

# 编译
build:
	go build -o ${BINARY_PATH} ${MAIN_PKG}

# 一键重新构建（config + api + wire + build）
rebuild: config api wire build

# 编译至 Linux AMD64 平台
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ${BINARY_PATH} ${MAIN_PKG}

# 运行
run:
	go run ${MAIN_PKG}

# 安装依赖
tidy:
	go mod tidy

# 格式化
fmt:
	gofmt -w .

# 验收：格式 + 静态检查 + 测试 + 生成物漂移检查（本地与 CI 同一条命令）
# 注意：-race 需要 cgo 与 C 编译器（gcc/clang）；Windows 请用 MinGW 或 WSL。
verify: fmt
	@test -z "$$(gofmt -l .)" || (echo "gofmt 有未格式化文件"; gofmt -l .; exit 1)
	go vet ./...
	go test -race -count=1 ./...
	$(MAKE) config api wire
	@git diff --exit-code -- . ':(exclude)go.sum' || (echo "生成物与源码不同步：请提交 make config api wire 的结果"; exit 1)

# 数据库迁移（编号 SQL，禁止 AutoMigrate）
# 默认跑 MySQL：app/<svc>/migrations/*.sql 里除 *_sqlite.sql 之外的全部按文件名排序执行。
migrate:
	@test -n "$(DB_NAME)" || (echo "用法: make migrate DB_NAME=rym [DB_HOST=127.0.0.1 DB_PORT=3306 DB_USER=root DB_PASSWORD=xxx]"; exit 1)
	@for f in $$(find app -path '*/migrations/*.sql' ! -name '*_sqlite.sql' | sort); do \
		echo "==> $$f"; \
		mysql -h $${DB_HOST:-127.0.0.1} -P $${DB_PORT:-3306} -u $${DB_USER:-root} $${DB_PASSWORD:+-p$${DB_PASSWORD}} $(DB_NAME) < $$f || exit 1; \
	done

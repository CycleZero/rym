// Package transport 负责 HTTP 传输层装配：kratos http.Server、中间件链、
// 统一响应/错误编码器、健康检查与 pprof 调试服务。
//
// 与本仓库分层的对应关系：
//   - 中间件顺序在这里写死一次（不再有包级可变开关 + panic 校验）
//   - 服务自己的路由由 app/<svc>/internal/server 通过生成物注册
package transport

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"gin-template/conf"
	"gin-template/pkg/errs"
	"gin-template/pkg/response"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// defaultServerTimeout kratos 默认处理超时只有 1s，对 DB 查询明显偏短，
// 这里显式放大（kratos 不接受标准库 http.Server 的读写超时配置）。
const defaultServerTimeout = 30 * time.Second

// ReadinessChecker 就绪检查函数：返回 nil 表示所有依赖可用。
type ReadinessChecker func(ctx context.Context) error

// NewHTTPServer 装配业务端口的 kratos http.Server。
func NewHTTPServer(
	cfg *conf.Bootstrap,
	mws *Middlewares,
	logger *slog.Logger,
	readiness ReadinessChecker,
) *khttp.Server {
	httpCfg := cfg.GetServer().GetHttp()

	opts := []khttp.ServerOption{
		khttp.Network("tcp"),
		khttp.Address(httpCfg.Addr()),
		khttp.Timeout(defaultServerTimeout),
		// 中间件：先注册的是最外层
		khttp.Middleware(mws.chain...),
		// 过滤器：访问日志 / OTel 埋点 / CORS（需要直接操作 ResponseWriter）
		khttp.Filter(mws.filters...),
		// 统一响应信封：成功包 {code,message,data,trace_id}，失败按 errs 映射状态码
		khttp.ResponseEncoder(response.Encode),
		khttp.ErrorEncoder(func(w http.ResponseWriter, r *http.Request, err error) {
			response.EncodeError(w, r, err, logger)
		}),
	}

	srv := khttp.NewServer(opts...)
	registerHealth(srv, readiness)
	return srv
}

// registerHealth 注册 K8s 探针语义的健康检查。
//
//   - /healthz 存活探针：进程能响应即 200，不检查依赖（避免依赖抖动引发重启）
//   - /readyz  就绪探针：检查 MySQL/Redis，不可用返回 503（摘流量但不重启）
func registerHealth(srv *khttp.Server, readiness ReadinessChecker) {
	r := srv.Route("/")

	r.GET("/healthz", func(ctx khttp.Context) error {
		return ctx.Result(http.StatusOK, map[string]string{"status": "ok"})
	})

	r.GET("/readyz", func(ctx khttp.Context) error {
		if readiness == nil {
			return ctx.Result(http.StatusOK, map[string]string{"status": "ready"})
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := readiness(probeCtx); err != nil {
			// 依赖不可用属服务端问题：503 + 稳定业务码，细节只进日志
			return errs.Unavailable("依赖未就绪").WithCause(err)
		}
		return ctx.Result(http.StatusOK, map[string]string{"status": "ready"})
	})
}

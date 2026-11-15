package main

import (
	"log/slog"
	"time"

	"gin-template/conf"
	"gin-template/pkg/module"
	"gin-template/pkg/transport"

	"github.com/go-kratos/kratos/v3"
	ktransport "github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// NewApp 组装 kratos.App：唯一进程入口，装载业务 HTTP 服务与可选的 pprof 服务。
//
// 生命周期（开始/停止顺序、信号处理、停机超时）由 kratos 管理；
// 遥测 flush 与资源释放放在 main，保证启动失败与正常退出两条路径都能释放。
func NewApp(
	cfg *conf.Bootstrap,
	logger *slog.Logger,
	hs *khttp.Server,
	pprofSrv *transport.PProfServer,
	mods *module.Set,
) *kratos.App {
	servers := []ktransport.Server{hs}
	if pprofSrv != nil {
		servers = append(servers, pprofSrv)
	}

	logger.Info("服务模块装配完成", "service", cfg.ServiceName(), "modules", mods.Names())
	if err := hs.WalkRoute(func(r khttp.RouteInfo) error {
		logger.Info("注册路由", "method", r.Method, "path", r.Path)
		return nil
	}); err != nil {
		logger.Warn("枚举路由失败", "error", err)
	}

	return kratos.New(
		kratos.Name(cfg.ServiceName()),
		kratos.Logger(logger),
		kratos.Server(servers...),
		kratos.StopTimeout(5*time.Second),
	)
}

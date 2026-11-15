package transport

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/CycleZero/rym/conf"
)

// PProfServer 独立的 pprof 调试服务（实现 kratos transport.Server）。
//
// 安全约定：pprof **只监听独立端口，绝不挂到业务端口**——它能读取进程内存
// （含堆上的令牌/密钥）。
type PProfServer struct {
	addr string
	srv  *http.Server
	log  *slog.Logger
}

// NewPProfServer 按配置构建调试服务；未启用时返回 nil。
//
//   - enable=false 或 port<=0 → nil（不会退化为挂到业务端口）
func NewPProfServer(cfg *conf.Bootstrap, logger *slog.Logger) *PProfServer {
	p := cfg.GetServer().GetHttp().GetPprof()
	if !p.GetEnable() || p.GetPort() <= 0 {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	addr := net.JoinHostPort(p.GetHost(), strconv.Itoa(int(p.GetPort())))
	return &PProfServer{
		addr: addr,
		log:  logger,
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

// Start 阻塞运行，直到 Stop 被调用（kratos.App 管理生命周期）。
func (p *PProfServer) Start(_ context.Context) error {
	p.log.Info("pprof 调试服务已启用", "addr", p.addr)
	if err := p.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 优雅关闭调试服务。
func (p *PProfServer) Stop(ctx context.Context) error {
	return p.srv.Shutdown(ctx)
}

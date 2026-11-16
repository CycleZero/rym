package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	democonf "github.com/CycleZero/rym/app/demo/conf"
	imconf "github.com/CycleZero/rym/app/im/conf"
	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/log"
	"github.com/CycleZero/rym/pkg/metrics"
	"github.com/CycleZero/rym/pkg/trace"
)

// shutdownTimeout 优雅停机的总预算：排空在途请求 + flush 遥测数据。
const shutdownTimeout = 5 * time.Second

func main() {
	confPath := flag.String("conf", conf.DefaultPath, "共享配置文件路径（文件或目录）")
	demoConfPath := flag.String("demo-conf", democonf.DefaultPath, "demo 服务配置文件路径（可选）")
	imConfPath := flag.String("im-conf", imconf.DefaultConfigPath, "im 服务配置文件路径（可选）")
	flag.Parse()

	// 1) 共享配置：不设全局，加载结果显式注入（见 initApp）
	bc, cfgSource, err := conf.Load(*confPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}

	// 2) 服务自有配置：每个服务一份，拆分后由该服务的 main 自行加载
	demoCfg, err := democonf.Load(*demoConfPath)
	if err != nil {
		_ = cfgSource.Close()
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
	imCfg, err := imconf.Load(*imConfPath)
	if err != nil {
		_ = cfgSource.Close()
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}

	// 3) 服务身份：trace / metrics / logs 三路共用同一个 Resource
	service := bc.OtelServiceInfo(hostname())
	tracesEndpoint, metricsEndpoint, logsEndpoint := otelEndpoints(bc.GetOtel())

	logger, err := log.New(logConfig(bc, service, logsEndpoint))
	if err != nil {
		_ = cfgSource.Close()
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
	slog.SetDefault(logger.Logger)

	// trace：始终安装 Provider —— 请求 ID 就是 TraceID，未接 OTLP 时也要能取到 ID
	tracerProvider, err := trace.Init(traceConfig(bc, service, tracesEndpoint))
	if err != nil {
		logger.Error("初始化 trace 失败，已降级为不上报 span", "error", err)
	}
	// metrics：端点为空时 Init 返回 nil，表示未启用（无开销）
	meterProvider, err := metrics.Init(metricsConfig(bc, service, metricsEndpoint))
	if err != nil {
		logger.Error("初始化 metrics 上报失败，已降级为不上报", "error", err)
	}

	// 4) 装配（Wire 生成）：此处的 DB 连接失败会返回 error 而不是 panic
	app, cleanup, err := initApp(bc, demoCfg, imCfg, logger.Logger)
	if err != nil {
		logger.Error("装配应用失败", "error", err)
		_ = logger.Close(context.Background())
		_ = cfgSource.Close()
		os.Exit(1)
	}

	// 5) 阻塞运行：kratos 内部处理 SIGINT/SIGTERM 并优雅停机
	if err := app.Run(); err != nil {
		logger.Error("服务异常退出", "error", err)
	} else {
		logger.Info("服务已停止")
	}

	// 6) 收尾顺序即依赖顺序：连接 → 遥测 → 日志（日志必须最后关）
	cleanup()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := trace.Shutdown(shutdownCtx, tracerProvider); err != nil {
		logger.Error("关闭 trace 上报失败", "error", err)
	}
	if err := metrics.Shutdown(shutdownCtx, meterProvider); err != nil {
		logger.Error("关闭 metrics 上报失败", "error", err)
	}
	if err := logger.Close(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, "关闭日志失败:", err)
	}
	if err := cfgSource.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "关闭配置失败:", err)
	}
}

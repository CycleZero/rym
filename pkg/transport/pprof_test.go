package transport_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/transport"
)

func pprofConfig(enable bool, host string, port uint32) *conf.Bootstrap {
	return &conf.Bootstrap{
		Server: &conf.Server{
			Http: &conf.HTTP{
				Pprof: &conf.Pprof{Enable: enable, Host: host, Port: port},
			},
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewPProfServerDisabled 未启用、或启用但没配端口时都不启动调试服务。
//
// 关键安全语义：端口未配置时**跳过**，而不是把 pprof 退化挂到业务端口上。
func TestNewPProfServerDisabled(t *testing.T) {
	if srv := transport.NewPProfServer(pprofConfig(false, "127.0.0.1", 6060), discardLogger()); srv != nil {
		t.Error("pprof.enable=false 时不应创建调试服务")
	}
	if srv := transport.NewPProfServer(pprofConfig(true, "127.0.0.1", 0), discardLogger()); srv != nil {
		t.Error("pprof.port 未配置时不应创建调试服务（不会挂到业务端口）")
	}
}

// TestPProfServerServes 真正把 pprof 跑起来并访问 /debug/pprof/。
func TestPProfServerServes(t *testing.T) {
	port := freePort(t)
	srv := transport.NewPProfServer(
		pprofConfig(true, "127.0.0.1", uint32(port)), discardLogger())
	if srv == nil {
		t.Fatal("pprof 启用且配置了端口时应创建调试服务")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.Start(ctx); err != nil {
			t.Errorf("pprof 服务异常退出：%v", err)
		}
	}()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
	})

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	client := &http.Client{Timeout: 5 * time.Second}

	// 服务刚启动，短暂重试等待监听就绪
	var body []byte
	var err error
	for range 20 {
		resp, getErr := client.Get("http://" + addr + "/debug/pprof/")
		if getErr == nil {
			body, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("状态码 = %d, want 200", resp.StatusCode)
			}
			err = nil
			break
		}
		err = getErr
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("访问 pprof 失败：%v", err)
	}
	if !strings.Contains(string(body), "goroutine") {
		t.Error("pprof 索引页未包含 goroutine profile，路由可能未正确注册")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败：%v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

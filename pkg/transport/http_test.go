package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gin-template/conf"
	"gin-template/pkg/otelx"
	"gin-template/pkg/response"
	"gin-template/pkg/trace"
	"gin-template/pkg/transport"
)

// newTestServer 装配一个真实的 kratos http.Server（仅依赖内存，无需 DB/Redis）。
func newTestServer(t *testing.T, readiness transport.ReadinessChecker, origins []string) *httptest.Server {
	t.Helper()

	provider, err := trace.Init(trace.Config{Service: otelx.ServiceInfo{Name: "transport-test"}})
	if err != nil {
		t.Fatalf("初始化 trace 失败：%v", err)
	}
	t.Cleanup(func() { _ = trace.Shutdown(context.Background(), provider) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bc := &conf.Bootstrap{
		Server: &conf.Server{Http: &conf.HTTP{
			Host:             "127.0.0.1",
			Port:             8000, // httptest 自己选端口，这里仅用于 Addr()
			CorsAllowOrigins: origins,
		}},
		Otel: &conf.Otel{ServiceName: "transport-test"},
	}

	srv := transport.NewHTTPServer(bc, transport.NewMiddlewares(bc, logger), logger, readiness)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string, mutate func(*http.Request)) (*http.Response, response.Body) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败：%v", err)
	}
	var body response.Body
	if len(raw) == 0 { // 204 等无响应体场景
		return resp, body
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析响应体失败：%v（原始内容 %s）", err, raw)
	}
	return resp, body
}

// TestHealthEndpointsClosedLoop 端到端验证"请求 ID 闭环"与健康检查契约：
//
//   - /healthz 200 + 统一信封
//   - /readyz 依赖不可用时 503/50300，且不泄漏内部细节
//   - 响应头 X-Request-ID ≡ 响应体 trace_id
func TestHealthEndpointsClosedLoop(t *testing.T) {
	ts := newTestServer(t, nil, nil)

	resp, body := get(t, ts, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", resp.StatusCode)
	}
	if body.Code != 0 || body.Message != "ok" {
		t.Errorf("信封 = %+v, want code=0 message=ok", body)
	}
	header := resp.Header.Get(response.HeaderRequestID)
	if header == "" {
		t.Error("响应头 X-Request-ID 不应为空")
	}
	if body.TraceID != header {
		t.Errorf("响应体 trace_id (%q) 与响应头 (%q) 不一致", body.TraceID, header)
	}
}

func TestReadinessFailure(t *testing.T) {
	ts := newTestServer(t, func(context.Context) error {
		return errors.New("mysql: dial tcp 127.0.0.1:3306: connect: connection refused")
	}, nil)

	resp, body := get(t, ts, "/readyz", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, want 503", resp.StatusCode)
	}
	if body.Code != 50300 || body.Message != "依赖未就绪" {
		t.Errorf("信封 = %+v, want code=50300 message=依赖未就绪", body)
	}
	if strings.Contains(body.TraceID, "3306") {
		t.Error("响应体泄漏了依赖细节")
	}
	if body.TraceID == "" {
		t.Error("失败响应同样要带 trace_id")
	}
}

// TestTraceIDContinuesUpstreamTraceparent 上游传 traceparent 时续接其 TraceID。
func TestTraceIDContinuesUpstreamTraceparent(t *testing.T) {
	ts := newTestServer(t, nil, nil)

	const upstreamTraceID = "5f0af201e4905c0eeab1603c31ab1540"
	resp, body := get(t, ts, "/healthz", func(r *http.Request) {
		r.Header.Set("traceparent", "00-"+upstreamTraceID+"-c9a26a0dddcd8b12-01")
	})

	if got := resp.Header.Get(response.HeaderRequestID); got != upstreamTraceID {
		t.Errorf("X-Request-ID = %q, want 上游 TraceID %q", got, upstreamTraceID)
	}
	if body.TraceID != upstreamTraceID {
		t.Errorf("响应体 trace_id = %q, want %q", body.TraceID, upstreamTraceID)
	}
}

func TestCORSPreflightAndWhitelist(t *testing.T) {
	const allowed = "https://app.example.com"
	ts := newTestServer(t, nil, []string{allowed})

	t.Run("命中白名单", func(t *testing.T) {
		resp, _ := get(t, ts, "/healthz", func(r *http.Request) {
			r.Header.Set("Origin", allowed)
		})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != allowed {
			t.Errorf("Allow-Origin = %q, want %q", got, allowed)
		}
		if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
			t.Errorf("Vary = %q, 应包含 Origin", got)
		}
	})

	t.Run("未命中白名单", func(t *testing.T) {
		resp, _ := get(t, ts, "/healthz", func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example.com")
		})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("未授权来源不应回显 Allow-Origin，实际 %q", got)
		}
	})

	t.Run("预检直接 204", func(t *testing.T) {
		resp, _ := get(t, ts, "/healthz", func(r *http.Request) {
			r.Method = http.MethodOptions
			r.Header.Set("Origin", allowed)
		})
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("状态码 = %d, want 204", resp.StatusCode)
		}
	})
}

package ws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/token"
	"github.com/CycleZero/rym/pkg/wsticket"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const testOrigin = "https://app.example.com"

func newTestServer(t *testing.T, cfg Config) (*Server, *Hub, *httptest.Server, string) {
	t.Helper()
	if cfg.Path == "" {
		cfg.Path = "/ws"
	}
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "ws-test-secret"
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{testOrigin}
	}
	cfg.Enable = true
	logger := testLogger()
	hub := NewHub(cfg, logger)
	tickets := wsticket.NewStore(cache.NewMemoryCache())
	srv := NewServer(cfg, hub, tickets, logger)
	ts := httptest.NewServer(srv.httpSrv.Handler)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + cfg.Path
	return srv, hub, ts, wsURL
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dialWithJWT(t *testing.T, wsURL string, uid uint64, origin string) *websocket.Conn {
	t.Helper()
	issuer, err := token.NewIssuer("ws-test-secret", time.Hour)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	raw, _, err := issuer.Issue(uid, "tester")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+raw)
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readType(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var event struct {
		V    int    `json:"v"`
		Type string `json:"type"`
	}
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatalf("read: %v", err)
	}
	if event.Type != want {
		t.Fatalf("event type=%q，期望 %q", event.Type, want)
	}
}

func TestJWTConnectAndPublish(t *testing.T) {
	_, hub, _, wsURL := newTestServer(t, Config{})
	conn := dialWithJWT(t, wsURL, 42, testOrigin)
	readType(t, conn, "hello")

	hub.Publish(42, []byte(`{"v":1,"type":"message.created","data":{"conversation_id":1}}`))
	readType(t, conn, "message.created")
}

func TestTicketConnectSingleUse(t *testing.T) {
	srv, _, ts, _ := newTestServer(t, Config{})
	ctx := context.Background()
	ticket, _, err := srv.tickets.Issue(ctx, 7, "")
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?ticket=" + ticket
	header := http.Header{}
	header.Set("Origin", testOrigin)
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if err != nil {
		t.Fatalf("ticket dial: %v", err)
	}
	readType(t, conn, "hello")
	_ = conn.CloseNow()

	// 同一张 ticket 第二次必须被拒绝（401）。
	dialCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	cancel()
	if err == nil {
		t.Fatal("重复使用 ticket 应失败")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("重复 ticket 应返回 401，实际 %+v", resp)
	}
}

func TestOriginDenied(t *testing.T) {
	_, _, _, wsURL := newTestServer(t, Config{})
	header := http.Header{}
	header.Set("Origin", "https://evil.example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		t.Fatal("非法 Origin 应失败")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("非法 Origin 应返回 403，实际 %+v", resp)
	}
}

func TestUnauthorized(t *testing.T) {
	_, _, _, wsURL := newTestServer(t, Config{})
	header := http.Header{}
	header.Set("Origin", testOrigin)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		t.Fatal("无令牌应失败")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应返回 401，实际 %+v", resp)
	}
}

func TestNewServerDisabled(t *testing.T) {
	if srv := NewServer(Config{}, NewHub(Config{}, testLogger()), nil, testLogger()); srv != nil {
		t.Fatal("Enable=false 时应返回 nil")
	}
}

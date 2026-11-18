package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/CycleZero/rym/pkg/token"
	"github.com/CycleZero/rym/pkg/wsticket"

	"github.com/coder/websocket"
	"github.com/google/wire"
)

const maxClientFrame = 4 << 10 // 客户端只发控制帧/心跳，4KB 足够

var errUnauthorized = errors.New("ws: unauthorized")

// 本地开发 + Tauri 的默认 Origin 白名单（与 pkg/transport 的 CORS 默认值一致）。
var defaultOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
	"http://127.0.0.1:5173",
	"tauri://localhost",
	"http://tauri.localhost",
}

// Config 是 WS 服务的静态配置（由 cmd/server 从 conf 转换，pkg 不依赖 conf）。
type Config struct {
	Enable          bool
	Addr            string
	Path            string
	JWTSecret       string
	AllowedOrigins  []string
	Heartbeat       time.Duration
	PongTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxLifetime     time.Duration
	MaxConnsPerUser int
}

func (c Config) withDefaults() Config {
	if c.Path == "" {
		c.Path = "/ws"
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8001"
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 25 * time.Second
	}
	if c.PongTimeout <= 0 {
		c.PongTimeout = 10 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = 24 * time.Hour
	}
	if c.MaxConnsPerUser <= 0 {
		c.MaxConnsPerUser = 5
	}
	if len(c.AllowedOrigins) == 0 {
		c.AllowedOrigins = defaultOrigins
	}
	return c
}

// Server 是独立的 WebSocket 监听器（实现 kratos transport.Server）。
//
// 独立端口的原因：长连接不适合 kratos HTTP 的响应信封/30s 超时；
// 也避免与 pkg/transport 的 ResponseWriter 包装层纠缠。
type Server struct {
	cfg     Config
	hub     *Hub
	tickets *wsticket.Store
	logger  *slog.Logger
	httpSrv *http.Server
}

// NewServer 构造 WS 服务；Enable=false 返回 nil（组合根会跳过）。
func NewServer(cfg Config, hub *Hub, tickets *wsticket.Store, logger *slog.Logger) *Server {
	if !cfg.Enable || hub == nil {
		return nil
	}
	cfg = cfg.withDefaults()

	mux := http.NewServeMux()
	s := &Server{
		cfg:     cfg,
		hub:     hub,
		tickets: tickets,
		logger:  logger,
	}
	mux.HandleFunc(cfg.Path, s.handle)
	s.httpSrv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return s
}

// Addr 返回监听地址（启动日志用）。
func (s *Server) Addr() string { return s.cfg.Addr }

// Start 阻塞运行，直到 Stop 被调用（kratos.App 管理生命周期）。
func (s *Server) Start(_ context.Context) error {
	s.logger.Info("WS 服务已启用", "addr", s.cfg.Addr, "path", s.cfg.Path)
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 先关闭所有连接，再停 HTTP 监听。
func (s *Server) Stop(ctx context.Context) error {
	s.hub.CloseAll(websocket.StatusGoingAway, "服务停机")
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.originAllowed(origin) {
		http.Error(w, "origin forbidden", http.StatusForbidden)
		return
	}

	uid, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin 已在上面显式校验
	})
	if err != nil {
		s.logger.Warn("WS 握手失败", "error", err, "remote", r.RemoteAddr)
		return
	}
	wsConn.SetReadLimit(maxClientFrame)

	conn := newConn(uid, wsConn, s)
	if !s.hub.TryRegister(conn) {
		_ = wsConn.Close(closeTooManyConns, "连接数超过上限")
		return
	}

	s.logger.Info("WS 已连接", "uid", uid, "conn_id", conn.id, "remote", r.RemoteAddr)
	conn.start()
	conn.enqueue(helloPayload(conn))
	<-conn.done
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		// 原生客户端（Wails/Flutter IO）通常不带 Origin。
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func (s *Server) authenticate(r *http.Request) (uint64, error) {
	if raw := bearerToken(r.Header.Get("Authorization")); raw != "" {
		claims, err := token.Verify(raw, s.cfg.JWTSecret)
		if err != nil {
			return 0, err
		}
		return claims.UserID, nil
	}

	if ticket := strings.TrimSpace(r.URL.Query().Get("ticket")); ticket != "" {
		if s.tickets == nil {
			return 0, errUnauthorized
		}
		return s.tickets.Redeem(r.Context(), ticket, r.Header.Get("Origin"))
	}
	return 0, errUnauthorized
}

func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

type helloData struct {
	ConnID         string `json:"conn_id"`
	ServerTime     string `json:"server_time"`
	HeartbeatSec   int64  `json:"heartbeat_sec"`
	MaxLifetimeSec int64  `json:"max_lifetime_sec"`
}

type helloEvent struct {
	V    int       `json:"v"`
	Type string    `json:"type"`
	Data helloData `json:"data"`
}

func helloPayload(c *Conn) []byte {
	raw, _ := json.Marshal(helloEvent{
		V:    1,
		Type: "hello",
		Data: helloData{
			ConnID:         c.id,
			ServerTime:     time.Now().Format(time.RFC3339),
			HeartbeatSec:   int64(c.cfg.Heartbeat / time.Second),
			MaxLifetimeSec: int64(c.cfg.MaxLifetime / time.Second),
		},
	})
	return raw
}

// ProviderSet 是 Wire 装配入口。
var ProviderSet = wire.NewSet(NewHub, NewServer)

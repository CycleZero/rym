package ws

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// 应用自定义 close code（4000-4999）。
const (
	closeLifetime     websocket.StatusCode = 4001
	closeSlowClient   websocket.StatusCode = 4008
	closeTooManyConns websocket.StatusCode = 4009
)

// Conn 是一条 WebSocket 连接。
//
// 读写各一个 goroutine：读侧只用 CloseRead 处理控制帧（本服务是推送型，
// 不接受业务上行消息）；写侧从有界队列取事件写出。
type Conn struct {
	id     string
	uid    uint64
	ws     *websocket.Conn
	send   chan []byte
	done   chan struct{}
	once   sync.Once
	hub    *Hub
	cfg    Config
	logger *slog.Logger
}

func newConn(uid uint64, ws *websocket.Conn, s *Server) *Conn {
	return &Conn{
		id:     newConnID(),
		uid:    uid,
		ws:     ws,
		send:   make(chan []byte, 64),
		done:   make(chan struct{}),
		hub:    s.hub,
		cfg:    s.cfg,
		logger: s.logger,
	}
}

func (c *Conn) start() {
	// CloseRead 处理 close/ping/pong 等控制帧，并返回连接关闭时 cancel 的 ctx。
	closed := c.ws.CloseRead(context.Background())
	go func() {
		<-closed.Done()
		c.close(websocket.StatusNormalClosure, "对端关闭")
	}()
	go c.writeLoop()
	go c.heartbeatLoop()
}

func (c *Conn) enqueue(payload []byte) bool {
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (c *Conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), c.cfg.WriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				c.close(websocket.StatusInternalError, "写失败")
				return
			}
		}
	}
}

func (c *Conn) heartbeatLoop() {
	heartbeat := c.cfg.Heartbeat
	if heartbeat <= 0 {
		heartbeat = 25 * time.Second
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	var lifetime <-chan time.Time
	if c.cfg.MaxLifetime > 0 {
		timer := time.NewTimer(c.cfg.MaxLifetime)
		defer timer.Stop()
		lifetime = timer.C
	}

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), c.cfg.PongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusPolicyViolation, "心跳超时")
				return
			}
		case <-lifetime:
			// 限制连接寿命：注销/禁用后最多 24h 失效，客户端重新取票据重连。
			c.close(closeLifetime, "连接已达最大生命周期")
			return
		}
	}
}

// close 幂等关闭连接；先摘注册表，再发 close 帧。
func (c *Conn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		c.hub.Unregister(c.uid, c.id)
		if c.logger != nil {
			c.logger.InfoContext(context.Background(), "WS 连接关闭",
				"uid", c.uid, "conn_id", c.id, "code", int(code), "reason", reason)
		}
		// Close 会做关闭握手，放到 goroutine，避免阻塞发布路径。
		go func() { _ = c.ws.Close(code, reason) }()
	})
}

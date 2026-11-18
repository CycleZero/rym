package ws

import (
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/coder/websocket"
)

// Hub 是单进程内的连接注册表：uid -> connID -> *Conn。
//
// 单体阶段够用；拆多实例时把 Publish 换成 Redis Pub/Sub（按 uid 频道），
// 连接对象仍然只存在持有它的进程里。
type Hub struct {
	mu         sync.RWMutex
	conns      map[uint64]map[string]*Conn
	maxPerUser int
	logger     *slog.Logger
}

// NewHub 创建连接表。maxPerUser<=0 时默认 5。
func NewHub(cfg Config, logger *slog.Logger) *Hub {
	max := cfg.MaxConnsPerUser
	if max <= 0 {
		max = 5
	}
	return &Hub{
		conns:      make(map[uint64]map[string]*Conn),
		maxPerUser: max,
		logger:     logger,
	}
}

// TryRegister 注册连接；超过单用户上限返回 false。
func (h *Hub) TryRegister(c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	set := h.conns[c.uid]
	if set == nil {
		set = make(map[string]*Conn)
		h.conns[c.uid] = set
	}
	if h.maxPerUser > 0 && len(set) >= h.maxPerUser {
		return false
	}
	set[c.id] = c
	return true
}

// Unregister 移除连接（幂等）。
func (h *Hub) Unregister(uid uint64, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	set := h.conns[uid]
	if set == nil {
		return
	}
	delete(set, id)
	if len(set) == 0 {
		delete(h.conns, uid)
	}
}

// Publish 向 uid 的所有在线连接推送；返回因队列满被丢弃的连接数。
//
// 非阻塞：一个慢客户端不能拖住发布者。队列满时直接断开该连接，
// 客户端重连后用 after_seq 补洞。
func (h *Hub) Publish(uid uint64, payload []byte) int {
	h.mu.RLock()
	set := h.conns[uid]
	conns := make([]*Conn, 0, len(set))
	for _, c := range set {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	dropped := 0
	for _, c := range conns {
		if !c.enqueue(payload) {
			dropped++
			go c.close(closeSlowClient, "客户端消费过慢")
		}
	}
	if dropped > 0 && h.logger != nil {
		h.logger.Warn("WS 推送丢弃：客户端队列已满", "uid", uid, "dropped", dropped)
	}
	return dropped
}

// CloseAll 关闭所有连接（停机用）。
func (h *Hub) CloseAll(code websocket.StatusCode, reason string) {
	h.mu.RLock()
	conns := make([]*Conn, 0, len(h.conns))
	for _, set := range h.conns {
		for _, c := range set {
			conns = append(conns, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range conns {
		c.close(code, reason)
	}
}

// Count 返回当前连接数（诊断用）。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, set := range h.conns {
		n += len(set)
	}
	return n
}

func newConnID() string {
	return uuid.NewString()
}

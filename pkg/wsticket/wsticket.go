// Package wsticket 实现 WebSocket 握手的 30 秒单次票据。
//
// 为什么需要票据：浏览器 WebSocket API 不能设置 Authorization 头，
// 把长期 JWT 放 URL 会进入代理/负载均衡/浏览器历史日志。票据是短期、
// 单次、可绑 Origin 的随机串，日志泄漏时通常已经失效或被兑换过。
package wsticket

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CycleZero/rym/pkg/cache"

	"github.com/google/wire"
)

const (
	// TTL 票据有效期：足够慢速移动网络完成握手，又让日志里的票据很快失效。
	TTL = 30 * time.Second
	// keyPrefix 避免与其他 Redis key 冲突；ticket 本身是 256-bit 随机串。
	keyPrefix = "rym:ws:ticket:"
	// issueAttempts 随机串碰撞时的重试次数（实际几乎不会发生）。
	issueAttempts = 3
)

// ErrInvalid 票据无效/过期/已使用/Origin 不匹配。
var ErrInvalid = errors.New("wsticket: 票据无效或已过期")

// Store 是票据的签发与兑换入口。
type Store struct {
	cache cache.Cache
}

// NewStore 由 Wire 注入缓存实现（生产为 Redis，测试用内存缓存）。
func NewStore(c cache.Cache) *Store {
	return &Store{cache: c}
}

// ticketPayload 存在 Redis 里的最小信息。
type ticketPayload struct {
	UID    uint64 `json:"uid"`
	Origin string `json:"origin,omitempty"`
}

// Issue 为 uid 签发一张单次票据，返回票据串与过期时间。
//
// origin 来自签发请求的 Origin 头；为空表示原生客户端（Wails/Flutter IO），
// 不绑定 Origin。票据存入缓存后只能被 GETDEL 兑换一次。
func (s *Store) Issue(ctx context.Context, uid uint64, origin string) (string, time.Time, error) {
	if s == nil || s.cache == nil {
		return "", time.Time{}, errors.New("wsticket: 缓存未初始化")
	}
	if uid == 0 {
		return "", time.Time{}, errors.New("wsticket: uid 不能为 0")
	}

	body, err := json.Marshal(ticketPayload{UID: uid, Origin: strings.TrimSpace(origin)})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("wsticket: 编码票据失败: %w", err)
	}

	for i := 0; i < issueAttempts; i++ {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", time.Time{}, fmt.Errorf("wsticket: 生成随机票据失败: %w", err)
		}
		ticket := base64.RawURLEncoding.EncodeToString(raw)

		ok, err := s.cache.SetNX(ctx, keyPrefix+ticket, body, TTL)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("wsticket: 写票据失败: %w", err)
		}
		if ok {
			return ticket, time.Now().Add(TTL), nil
		}
	}
	return "", time.Time{}, errors.New("wsticket: 票据生成冲突")
}

// Redeem 原子兑换票据（GETDEL），成功返回 uid。
//
// 如果签发时绑定了 Origin，兑换时也必须匹配；任一侧为空都视为原生客户端。
func (s *Store) Redeem(ctx context.Context, ticket, origin string) (uint64, error) {
	if s == nil || s.cache == nil || strings.TrimSpace(ticket) == "" {
		return 0, ErrInvalid
	}

	data, err := s.cache.GetDel(ctx, keyPrefix+ticket)
	if err != nil {
		// 不存在/过期/Redis 故障都按无效票据处理：鉴权失败必须 fail-closed。
		return 0, ErrInvalid
	}
	var p ticketPayload
	if err := json.Unmarshal(data, &p); err != nil || p.UID == 0 {
		return 0, ErrInvalid
	}
	if p.Origin != "" && origin != "" && !strings.EqualFold(p.Origin, origin) {
		return 0, ErrInvalid
	}
	return p.UID, nil
}

// ProviderSet 是 Wire 装配入口。
var ProviderSet = wire.NewSet(NewStore)

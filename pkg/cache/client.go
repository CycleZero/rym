package cache

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"gin-template/pkg/trace"

	"github.com/redis/go-redis/v9"
)

// 连接池默认值：默认 10×CPU 对单体多服务过大，这里显式收敛。
const (
	defaultRedisPoolSize     = 20
	defaultRedisMinIdleConns = 5
)

// RedisParams Redis 连接参数（原始类型，不依赖任何配置包）。
type RedisParams struct {
	Host         string
	Port         int
	Username     string
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
}

// NewRedisClient 建立 Redis 客户端并返回关闭函数（Wire 会在退出时调用）。
//
// 这里集中挂载 OTel 追踪 Hook：缓存读写自动产生 span 与耗时指标，业务侧无感。
// 不在启动期 Ping：连不上由 /readyz 暴露，避免依赖抖动导致进程启动失败。
func NewRedisClient(p RedisParams) (*redis.Client, func(), error) {
	client := redis.NewClient(&redis.Options{
		Addr:         net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Username:     p.Username,
		Password:     p.Password,
		DB:           p.DB,
		PoolSize:     defaultInt(p.PoolSize, defaultRedisPoolSize),
		MinIdleConns: defaultInt(p.MinIdleConns, defaultRedisMinIdleConns),
	})
	client.AddHook(trace.NewRedisHook())

	return client, func() { _ = client.Close() }, nil
}

// Ping 探测 Redis 可用性（供 /readyz 组合）。
func Ping(ctx context.Context, client *redis.Client) error {
	if client == nil {
		return fmt.Errorf("cache: Redis 未初始化")
	}
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

func defaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

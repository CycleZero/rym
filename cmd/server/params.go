package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/configx"
	"github.com/CycleZero/rym/pkg/db"
	"github.com/CycleZero/rym/pkg/transport"
	transportws "github.com/CycleZero/rym/pkg/transport/ws"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// newDBParams 把共享配置转换为数据库连接参数。
//
// 放在组合根做转换：pkg/db 不依赖任何配置类型，拆分微服务时
// 每个服务传入自己的参数即可。
func newDBParams(bc *conf.Bootstrap) db.DBParams {
	dbCfg := bc.GetData().GetDb()
	return db.DBParams{
		DSN:             dbCfg.DSN(),
		MaxOpenConns:    int(dbCfg.GetMaxOpenConns()),
		MaxIdleConns:    int(dbCfg.GetMaxIdleConns()),
		ConnMaxLifetime: time.Duration(dbCfg.GetConnMaxLifetimeSeconds()) * time.Second,
		ConnMaxIdleTime: time.Duration(dbCfg.GetConnMaxIdleTimeSeconds()) * time.Second,
		Debug:           dbCfg.GetDebug(),
	}
}

// newRedisParams 把共享配置转换为 Redis 连接参数。
func newRedisParams(bc *conf.Bootstrap) cache.RedisParams {
	redisCfg := bc.GetData().GetRedis()
	return cache.RedisParams{
		Host:     redisCfg.GetHost(),
		Port:     int(redisCfg.GetPort()),
		Password: redisCfg.GetPassword(),
	}
}

// newCache 把 Redis 客户端适配成 wsticket 使用的通用缓存接口。
func newCache(rdb *redis.Client) cache.Cache {
	return cache.NewRedisCacheWithClient(rdb)
}

// newWSConfig 把共享配置转换为 WS 监听参数。
//
// 路径固定 /v1/im/ws；host/port 缺省 0.0.0.0:8001。
// Origin 白名单复用 server.http.cors_allow_origins。
func newWSConfig(bc *conf.Bootstrap) transportws.Config {
	w := bc.GetServer().GetWs()
	if !w.GetEnable() {
		return transportws.Config{}
	}
	host := w.GetHost()
	if host == "" {
		host = "0.0.0.0"
	}
	port := w.GetPort()
	if port == 0 {
		port = 8001
	}
	return transportws.Config{
		Enable:          true,
		Addr:            net.JoinHostPort(host, strconv.Itoa(int(port))),
		Path:            "/v1/im/ws",
		JWTSecret:       bc.GetSecurity().GetJwtSecret(),
		AllowedOrigins:  bc.GetServer().GetHttp().GetCorsAllowOrigins(),
		Heartbeat:       time.Duration(w.GetHeartbeatSeconds()) * time.Second,
		PongTimeout:     time.Duration(w.GetPongTimeoutSeconds()) * time.Second,
		WriteTimeout:    10 * time.Second,
		MaxLifetime:     time.Duration(w.GetMaxLifetimeSeconds()) * time.Second,
		MaxConnsPerUser: int(w.GetMaxConnectionsPerUser()),
	}
}

// newEtcdParams 把共享配置转换为 etcd 连接参数。
//
// endpoints 为空表示不启用 etcd：各服务退回本地 configs/<svc>/config.yaml。
func newEtcdParams(bc *conf.Bootstrap) configx.EtcdParams {
	etcdCfg := bc.GetEtcd()
	return configx.EtcdParams{
		Endpoints:   etcdCfg.EndpointList(),
		DialTimeout: time.Duration(etcdCfg.GetDialTimeoutSeconds()) * time.Second,
	}
}

// newAppContext 是进程级生命周期 context。
//
// 停机时 cancel：配置 watch goroutine 收到信号后退出。
// 它不是请求 context，只用于长驻后台任务。
func newAppContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	return ctx, cancel
}

// newReadinessChecker 组合各依赖的探测，作为 /readyz 的就绪检查。
//
// 探测顺序与命名在这里显式声明：错误只进日志，响应给客户端的是统一 503。
func newReadinessChecker(gdb *gorm.DB, rdb *redis.Client) transport.ReadinessChecker {
	return func(ctx context.Context) error {
		var problems []error
		if err := db.Ping(ctx, gdb); err != nil {
			problems = append(problems, err)
		}
		if err := cache.Ping(ctx, rdb); err != nil {
			problems = append(problems, err)
		}
		return errors.Join(problems...)
	}
}

package main

import (
	"context"
	"errors"
	"time"

	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/db"
	"github.com/CycleZero/rym/pkg/transport"

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

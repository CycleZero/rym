// Package db 提供 MySQL 连接（GORM）与健康探测。
//
// **不 import 任何配置类型**：构造参数是原始类型（DBParams），由组合根
// （cmd/server）把 conf.Bootstrap 转成参数注入。这样拆分微服务时每个服务
// 都能用自己的连接参数、启动自己的连接池。
package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 连接池默认值：长连接场景下的显式取值，可由配置覆盖。
const (
	DefaultMaxOpenConns    = 50
	DefaultMaxIdleConns    = 10
	DefaultConnMaxLifetime = 30 * time.Minute // 短于 MySQL wait_timeout
	DefaultConnMaxIdleTime = 10 * time.Minute
)

// DBParams MySQL 连接参数。
type DBParams struct {
	DSN string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration

	// Debug 为 true 时输出每条 SQL；生产默认 false（只记 Warn/Error）。
	Debug bool
}

// NewDB 按参数建立 MySQL 连接池，并返回关闭函数（Wire 会在退出时调用）。
func NewDB(p DBParams) (*gorm.DB, func(), error) {
	db, err := gorm.Open(mysql.Open(p.DSN), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   newGormLogger(p.Debug),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("获取数据库连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(defaultInt(p.MaxOpenConns, DefaultMaxOpenConns))
	sqlDB.SetMaxIdleConns(defaultInt(p.MaxIdleConns, DefaultMaxIdleConns))
	sqlDB.SetConnMaxLifetime(defaultDuration(p.ConnMaxLifetime, DefaultConnMaxLifetime))
	sqlDB.SetConnMaxIdleTime(defaultDuration(p.ConnMaxIdleTime, DefaultConnMaxIdleTime))

	return db, func() { _ = sqlDB.Close() }, nil
}

// Ping 探测数据库可用性（供 /readyz 组合）。
func Ping(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db: 连接未初始化")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	return nil
}

func defaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func defaultDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

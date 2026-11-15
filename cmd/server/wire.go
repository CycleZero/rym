//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/CycleZero/rym/app/demo"
	democonf "github.com/CycleZero/rym/app/demo/conf"
	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/db"
	"github.com/CycleZero/rym/pkg/transport"
	"github.com/CycleZero/rym/pkg/tx"

	"github.com/go-kratos/kratos/v3"
	"github.com/google/wire"
)

// initApp 是单体阶段的唯一组合根。
//
// 只允许出现：共享基础设施（pkg/*）+ 各服务的公开装配面（app/<svc>）。
// 任何 app/<svc>/internal/* 都进不来（Go internal 规则 + 设计约束）。
func initApp(
	bc *conf.Bootstrap,
	demoCfg *democonf.Config,
	logger *slog.Logger,
) (*kratos.App, func(), error) {
	panic(wire.Build(
		NewApp,
		newDBParams,
		newRedisParams,
		newReadinessChecker,
		db.ProviderSet,    // *gorm.DB + 关闭函数
		cache.ProviderSet, // *redis.Client + 关闭函数
		tx.NewTxManager,
		transport.ProviderSet,
		demo.ProviderSet, // ← 新增服务：加一行
		newModuleSet,     // ← 新增服务：modules.go 加一个参数
	))
}

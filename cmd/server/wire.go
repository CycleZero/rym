//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"gin-template/app/demo"
	democonf "gin-template/app/demo/conf"
	"gin-template/conf"
	"gin-template/pkg/cache"
	"gin-template/pkg/db"
	"gin-template/pkg/transport"
	"gin-template/pkg/tx"

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

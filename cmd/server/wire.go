//go:build wireinject
// +build wireinject

package main

import (
	"log/slog"

	"github.com/CycleZero/rym/app/demo"
	democonf "github.com/CycleZero/rym/app/demo/conf"
	"github.com/CycleZero/rym/app/im"
	imconf "github.com/CycleZero/rym/app/im/conf"
	"github.com/CycleZero/rym/app/user"
	userconf "github.com/CycleZero/rym/app/user/conf"
	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/configx"
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
	imCfg *imconf.Config,
	userCfg *userconf.Config,
	logger *slog.Logger,
) (*kratos.App, func(), error) {
	panic(wire.Build(
		NewApp,
		newAppContext, // 进程级 context，停机 cancel（配置 watch 退出）
		newDBParams,
		newRedisParams,
		newEtcdParams,
		newReadinessChecker,
		db.ProviderSet,      // *gorm.DB + 关闭函数
		cache.ProviderSet,   // *redis.Client + 关闭函数
		configx.ProviderSet, // *etcd.Client + 关闭函数
		tx.NewTxManager,
		transport.ProviderSet,
		demo.ProviderSet, // ← 新增服务：加一行
		im.ProviderSet,   // ← IM 服务
		user.ProviderSet, // ← 用户服务
		newModuleSet,     // ← 新增服务：modules.go 加一个参数
	))
}

//go:build wireinject
// +build wireinject

package im

import (
	"context"
	"log/slog"

	"github.com/CycleZero/rym/app/im/conf"
	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/app/im/internal/data"
	"github.com/CycleZero/rym/app/im/internal/server"
	"github.com/CycleZero/rym/app/im/internal/service"
	imws "github.com/CycleZero/rym/app/im/internal/ws"
	rootconf "github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/tx"
	"github.com/CycleZero/rym/pkg/wsticket"

	transportws "github.com/CycleZero/rym/pkg/transport/ws"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/google/wire"
	clientv3 "go.etcd.io/etcd/client/v3"
	"gorm.io/gorm"
)

// Provide 是 IM 服务的 Wire injector。
//
// 签名只允许出现「公开类型 + 共享基础设施」：
//   - internal/* 类型一律不能出现在参数或返回值里
//   - 否则组合根无法引用（Go internal 规则）
//
// ctx 由组合根创建并在停机时 cancel，用于停止配置 watch goroutine。
func Provide(
	ctx context.Context,
	cfg *conf.Config,
	bc *rootconf.Bootstrap,
	etcd *clientv3.Client,
	hub *transportws.Hub,
	tickets *wsticket.Store,
	srv *khttp.Server,
	db *gorm.DB,
	tm tx.TxManager,
	logger *slog.Logger,
) (*Module, error) {
	panic(wire.Build(
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		server.ProviderSet,
		NewStore,
		NewSettingsProvider,
		imws.NewPublisher,
		wire.Bind(new(service.TicketIssuer), new(*wsticket.Store)),
		newModule,
	))
}

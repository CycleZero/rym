//go:build wireinject
// +build wireinject

package user

import (
	"log/slog"

	"github.com/CycleZero/rym/app/user/conf"
	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/app/user/internal/data"
	"github.com/CycleZero/rym/app/user/internal/server"
	"github.com/CycleZero/rym/app/user/internal/service"
	rootconf "github.com/CycleZero/rym/conf"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/google/wire"
	"gorm.io/gorm"
)

// Provide 是用户服务的 Wire injector。
//
// 签名只允许出现「公开类型 + 共享基础设施」：
//   - internal/* 类型一律不能出现在参数或返回值里
//   - 否则组合根无法引用（Go internal 规则）
func Provide(
	cfg *conf.Config,
	bc *rootconf.Bootstrap,
	srv *khttp.Server,
	db *gorm.DB,
	logger *slog.Logger,
) (*Module, error) {
	panic(wire.Build(
		data.ProviderSet,
		biz.ProviderSet,
		service.ProviderSet,
		server.ProviderSet,
		NewSettings,
		NewTokenIssuer,
		newModule,
	))
}

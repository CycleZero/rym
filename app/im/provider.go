// Package im 是 IM 服务的**公开装配面**。
//
// 它只暴露两类东西：
//   - Module：组合根能看到的服务类型（不含任何 internal 类型）
//   - ProviderSet：该服务的 Wire injector（实现在 wire_gen.go）
//
// internal/ 是真正的黑盒：biz/data/service/server 全部不可被 cmd/server 引用，
// 从而保证 app/im 整个目录可以整体 mv 出去成为独立微服务。
package im

import (
	"context"
	"log/slog"

	"github.com/CycleZero/rym/app/im/conf"
	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/app/im/internal/server"
	rootconf "github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/configx"

	"github.com/google/wire"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Module 对外暴露的服务模块（不暴露 internal 类型）。
type Module struct {
	name string
}

// Name 返回服务模块名。
func (m *Module) Name() string { return m.name }

// newModule 把 internal 类型收敛成公开类型。
//
// 注意：不能用 `type Module = server.Module` 别名——Wire 生成的类型字符串
// 会带上 internal 包名，等于把 internal 泄漏给组合根。
func newModule(s *server.Module) *Module {
	return &Module{name: s.Name()}
}

// ConfigStore 是 im 配置的运行时快照。
//
// 不直接把 configx.Store[*conf.Config] 暴露给 Wire：包一层具体类型，
// 避免 Wire 生成物里出现泛型实例化，读起来也更直观。
type ConfigStore struct {
	store *configx.Store[*conf.Config]
}

// Current 返回当前配置快照（只读）。
func (s *ConfigStore) Current() *conf.Config {
	return s.store.Current()
}

// NewStore 构造配置快照。
//
//   - etcd 未启用（client == nil）：用启动时从本地文件加载的 initial，
//     不 watch；本地开发改文件后重启生效。
//   - etcd 已启用：从 <prefix>/<env>/configs/im/config.yaml 读取并 watch。
//     启动时 key 不存在 / 校验失败 → 启动失败（fail-closed）。
func NewStore(
	ctx context.Context,
	initial *conf.Config,
	bc *rootconf.Bootstrap,
	etcd *clientv3.Client,
	logger *slog.Logger,
) (*ConfigStore, error) {
	if initial == nil {
		// 防御性兜底：main 正常会传非 nil，但这里不能把 nil 存进快照。
		initial = conf.Default()
	}
	if etcd == nil {
		return &ConfigStore{store: configx.NewStore(initial)}, nil
	}

	key := configx.Key(bc.GetEtcd().GetPrefix(), bc.GetEtcd().GetEnv(), "im", "config.yaml")
	store, err := configx.Load(ctx, configx.Options[*conf.Config]{
		Scope:  key,
		Source: configx.NewEtcdSource(etcd, key),
		Decode: func(raw []byte) (*conf.Config, error) {
			return configx.DecodeProto(raw, func() *conf.Config { return &conf.Config{} })
		},
		Validate: func(c *conf.Config) error { return c.Validate() },
		Logger:   logger,
	})
	if err != nil {
		return nil, err
	}
	return &ConfigStore{store: store}, nil
}

// settingsProvider 把配置快照转换为 biz 需要的 DTO（biz 不 import conf）。
type settingsProvider struct {
	store *ConfigStore
}

// Settings 返回当前配置快照。每个用例入口调用一次，保证请求内一致。
func (p *settingsProvider) Settings() biz.Settings {
	c := p.store.Current()
	if c == nil {
		c = conf.Default()
	}
	return biz.Settings{
		DefaultPageSize: int(c.GetDefaultPageSize()),
		MaxPageSize:     int(c.GetMaxPageSize()),
		MaxBodyBytes:    int(c.GetMaxBodyBytes()),
	}
}

// NewSettingsProvider 是 biz.SettingsProvider 的实现。
func NewSettingsProvider(store *ConfigStore) biz.SettingsProvider {
	return &settingsProvider{store: store}
}

// ProviderSet 是该服务的唯一装配入口。
var ProviderSet = wire.NewSet(Provide)

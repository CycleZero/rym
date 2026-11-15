// Package demo 是 demo 服务的**公开装配面**。
//
// 它只暴露两类东西：
//   - Module：组合根能看到的服务类型（不含任何 internal 类型）
//   - ProviderSet：该服务的 Wire injector（实现在 wire_gen.go）
//
// internal/ 是真正的黑盒：biz/data/service/server 全部不可被 cmd/server 引用，
// 从而保证 app/demo 整个目录可以整体 mv 出去成为独立微服务。
package demo

import (
	"github.com/CycleZero/rym/app/demo/conf"
	"github.com/CycleZero/rym/app/demo/internal/biz"
	"github.com/CycleZero/rym/app/demo/internal/server"

	"github.com/google/wire"
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

// NewSettings 把服务自有配置转换为 biz 需要的设置（biz 不 import conf）。
func NewSettings(c *conf.Config) biz.Settings {
	return biz.Settings{
		DefaultPageSize: int(c.GetDefaultPageSize()),
		MaxPageSize:     int(c.GetMaxPageSize()),
	}
}

// ProviderSet 是该服务的唯一装配入口。
var ProviderSet = wire.NewSet(Provide)

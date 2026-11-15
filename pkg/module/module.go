// Package module 提供"服务模块集合"的最小抽象。
//
// Wire 不支持同一类型的多重绑定，因此模块聚合必须有一个显式缝
// （见 cmd/server/modules.go）：新增服务只改一处，而不是引入 ServiceHub
// 这种上帝对象。
package module

// Module 任一服务模块都满足的契约。
type Module interface {
	Name() string
}

// Set 进程内已装配的服务模块集合。
type Set struct {
	list []Module
}

// New 按声明顺序聚合模块。
func New(modules ...Module) *Set {
	return &Set{list: modules}
}

// Names 返回模块名（启动日志与路由诊断用）。
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.list))
	for _, m := range s.list {
		if m != nil {
			names = append(names, m.Name())
		}
	}
	return names
}

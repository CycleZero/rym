package main

import (
	"github.com/CycleZero/rym/app/demo"
	"github.com/CycleZero/rym/pkg/module"
)

// newModuleSet 是**唯一的模块聚合缝**。
//
// Wire 不支持同一类型的多重绑定，因此新增一个服务需要改两处：
//  1. wire.go 的 wire.Build 里加一行 foo.ProviderSet
//  2. 这里加一个参数并传给 module.New
//
// 其余依赖（DB/Redis/事务/中间件/编码器）全部由 Wire 按依赖图解析，
// 不再需要 ServiceHub 或包级注册表。
func newModuleSet(demoModule *demo.Module) *module.Set {
	return module.New(
		demoModule,
	)
}

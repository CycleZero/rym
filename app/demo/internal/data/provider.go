package data

import (
	"github.com/CycleZero/rym/app/demo/internal/biz"

	"github.com/google/wire"
)

// ProviderSet data 层。
//
// 用 wire.Bind 把具体实现绑定到 biz 声明的接口：biz 不 import data，
// 依赖方向单向（data → biz）。
var ProviderSet = wire.NewSet(
	NewDemoRepo,
	wire.Bind(new(biz.Repo), new(*demoRepo)),
)

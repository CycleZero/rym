package service

import (
	v1 "github.com/CycleZero/rym/api/im/v1"

	"github.com/google/wire"
)

// ProviderSet service 层。
//
// 绑定到生成物声明的 HTTP 接口：server 层只依赖接口（便于测试与未来换传输）。
var ProviderSet = wire.NewSet(
	NewImService,
	wire.Bind(new(v1.ImServiceHTTPServer), new(*ImService)),
)

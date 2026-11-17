package service

import (
	v1 "github.com/CycleZero/rym/api/user/v1"

	"github.com/google/wire"
)

// ProviderSet service 层。
//
// 绑定到生成物声明的 HTTP 接口：server 层只依赖接口（便于测试与未来换传输）。
var ProviderSet = wire.NewSet(
	NewUserService,
	wire.Bind(new(v1.UserServiceHTTPServer), new(*UserService)),
)

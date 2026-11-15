package transport

import "github.com/google/wire"

// ProviderSet 提供 HTTP 传输层装配（中间件、kratos server、pprof、就绪检查）。
var ProviderSet = wire.NewSet(
	NewMiddlewares,
	NewHTTPServer,
	NewPProfServer,
)

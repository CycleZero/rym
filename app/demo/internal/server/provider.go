package server

import "github.com/google/wire"

// ProviderSet server 层。
var ProviderSet = wire.NewSet(New)

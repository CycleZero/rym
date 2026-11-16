package biz

import "github.com/google/wire"

// ProviderSet biz 层。
var ProviderSet = wire.NewSet(NewUsecase)

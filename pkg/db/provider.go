package db

import "github.com/google/wire"

// ProviderSet 提供数据库连接（*gorm.DB）与关闭函数。
//
// 参数转换（conf.Bootstrap → DBParams）放在组合根 cmd/server，
// 本包因此不依赖任何配置类型。
var ProviderSet = wire.NewSet(NewDB)

package cache

import "github.com/google/wire"

// ProviderSet 提供 Redis 客户端（*redis.Client）与关闭函数。
//
// 参数转换（conf.Bootstrap → RedisParams）放在组合根 cmd/server，
// 本包因此不依赖任何配置类型。
var ProviderSet = wire.NewSet(NewRedisClient)

// Package common 存放跨层共享的**请求级元数据**（与传输框架无关）。
//
// 元数据由 transport 层中间件写入 context，biz/data 只读取，
// 因此这里不 import gin / kratos，HTTP 与 gRPC 可共用同一套。
package common

import "context"

// ctxKey 为不可导出类型，避免与其他包的 context key 冲突。
type ctxKey struct{}

var metadataKey ctxKey

// RequestMetadata 请求元数据。
type RequestMetadata struct {
	// UserID 当前用户；0 表示匿名。由鉴权中间件（网关头或本地 JWT）填充。
	UserID uint

	ClientIP  string
	UserAgent string

	// TraceID 链路 TraceID，同时也是本项目的"请求 ID"：
	// 响应头 X-Request-ID、响应体 trace_id、日志 trace.id 都是它。
	TraceID string
}

// WithMetadata 把元数据写入 context（存指针，允许后续中间件补写 UserID）。
func WithMetadata(ctx context.Context, md *RequestMetadata) context.Context {
	if md == nil {
		return ctx
	}
	return context.WithValue(ctx, metadataKey, md)
}

// Metadata 读取请求元数据；缺失时返回零值（而不是 nil），调用方可直接取字段。
func Metadata(ctx context.Context) *RequestMetadata {
	if md, ok := ctx.Value(metadataKey).(*RequestMetadata); ok && md != nil {
		return md
	}
	return &RequestMetadata{}
}

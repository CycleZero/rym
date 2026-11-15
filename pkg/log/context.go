package log

import (
	"context"
	"log/slog"
)

// FromContext 返回注入了链路字段（trace.id / span.id）的默认 logger。
//
// 本包不保存全局 Logger（由入口 main 调用 slog.SetDefault 注入），
// 因此这里取的是进程默认 logger + ctx 里的链路字段：
//
//	log.FromContext(ctx).Error("sql 执行失败", "error", err)
//
// 与 (*Logger).Ctx 的区别：后者用于显式持有 Logger 句柄的场景（如 main），
// 本函数用于只拿到 context 的基础设施适配器（如 GORM logger、HTTP 过滤器）。
func FromContext(ctx context.Context) *slog.Logger {
	return WithContext(slog.Default(), ctx)
}

// Package response 统一 HTTP 响应信封（与框架解耦，只依赖 net/http）。
//
// 成功响应：业务数据全部放在 data 字段下：
//
//	{ "code": 0, "message": "ok", "data": { ... }, "trace_id": "..." }
//
// 失败响应：不返回 data，只给稳定业务码与可对外消息：
//
//	{ "code": 40400, "message": "记录不存在", "trace_id": "..." }
//
// 本包只提供编码器，不持有全局状态：由 transport 层把 Encode/EncodeError
// 挂到 kratos http.Server 的 ResponseEncoder/ErrorEncoder 上。
//
// 终态说明：接入网关后信封由网关统一包装（wrapresp），服务可直接返回裸 proto；
// 单体阶段由本包兜底，保持现有客户端的响应契约不变。
package response

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CycleZero/rym/pkg/errs"
	"github.com/CycleZero/rym/pkg/log"
	"github.com/CycleZero/rym/pkg/trace"
)

// HeaderRequestID 响应头：值与响应体 trace_id 同源。
const HeaderRequestID = "X-Request-ID"

// Body 统一响应体。
type Body struct {
	Code    int    `json:"code"`               // 0 表示成功
	Message string `json:"message"`            // 成功固定为 ok
	Data    any    `json:"data,omitempty"`     // 业务数据（失败时不返回）
	TraceID string `json:"trace_id,omitempty"` // 链路 TraceID
}

// Encode 是 kratos http.Server 的 ResponseEncoder：成功响应统一包进信封。
func Encode(w http.ResponseWriter, r *http.Request, v any) error {
	return write(w, http.StatusOK, Body{
		Code:    errs.CodeOK,
		Message: "ok",
		Data:    v,
		TraceID: traceID(r.Context()),
	})
}

// EncodeError 是 kratos http.Server 的 ErrorEncoder。
//
// 5xx 记录 error 级日志（含内部 cause，不含响应体）；4xx 只给稳定消息，
// 避免把 SQL / 连接串等细节下发，也避免 4xx 污染告警。
func EncodeError(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	status, code, message := errs.Resolve(err)
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}

	if status >= http.StatusInternalServerError {
		fields := []any{"status", status, "code", code, "error", err}
		if r != nil {
			fields = append(fields, "method", r.Method, "path", r.URL.Path)
		}
		if logger != nil {
			log.WithContext(logger, ctx).Error("服务端错误", fields...)
		}
	}
	// 4xx 不写日志：请求方错误不应制造告警噪声（与原 response.Fail 语义一致）。

	_ = write(w, status, Body{Code: code, Message: message, TraceID: traceID(ctx)})
}

// write 输出响应体。
func write(w http.ResponseWriter, status int, body Body) error {
	if w == nil {
		return nil
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(body)
}

// traceID 取当前请求的链路 TraceID（由 OTel 埋点中间件写入 context）。
func traceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return trace.TraceID(ctx)
}

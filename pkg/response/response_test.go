package response

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CycleZero/rym/pkg/errs"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// requestWithSpan 构造带真实 span 的请求（trace_id 由 OTel Provider 生成）。
func requestWithSpan(t *testing.T) *http.Request {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	ctx, span := tp.Tracer("response-test").Start(context.Background(), "test")
	t.Cleanup(func() { span.End() })

	return httptest.NewRequest(http.MethodGet, "/t", nil).WithContext(ctx)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) Body {
	t.Helper()
	var body Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应体失败：%v（原始内容 %s）", err, rec.Body.String())
	}
	return body
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestEncodeSuccessEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Encode(rec, requestWithSpan(t), map[string]any{"id": 1}); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if body.Code != 0 || body.Message != "ok" {
		t.Errorf("信封 = %+v, want code=0 message=ok", body)
	}
	if body.Data == nil {
		t.Error("成功响应应把业务数据放在 data 字段下")
	}
	if body.TraceID == "" {
		t.Error("成功响应应带 trace_id（请求 ID 闭环）")
	}
}

func TestEncodeErrorClientError(t *testing.T) {
	rec := httptest.NewRecorder()
	EncodeError(rec, requestWithSpan(t), errs.NotFound("记录不存在"), silentLogger())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}
	body := decode(t, rec)
	if body.Code != errs.CodeNotFound || body.Message != "记录不存在" {
		t.Errorf("信封 = %+v, want code=%d message=记录不存在", body, errs.CodeNotFound)
	}
	if body.Data != nil {
		t.Error("失败响应不应带 data")
	}
	if body.TraceID == "" {
		t.Error("失败响应同样要带 trace_id")
	}
}

// TestEncodeErrorHidesInternalCause 500 只能下发固定文案，绝不泄漏内部细节。
func TestEncodeErrorHidesInternalCause(t *testing.T) {
	rec := httptest.NewRecorder()
	err := errs.Internal("服务器内部错误").WithCause(errors.New("dial tcp 127.0.0.1:3306: connection refused"))
	EncodeError(rec, requestWithSpan(t), err, silentLogger())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "3306") {
		t.Errorf("响应体泄漏了内部细节：%s", rec.Body.String())
	}
}

// TestEncodeErrorUnmappedError 未归一化的 error 一律 500 + 固定文案。
func TestEncodeErrorUnmappedError(t *testing.T) {
	rec := httptest.NewRecorder()
	EncodeError(rec, requestWithSpan(t), errors.New("boom"), silentLogger())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	if body := decode(t, rec); body.Code != errs.CodeInternal {
		t.Errorf("业务码 = %d, want %d", body.Code, errs.CodeInternal)
	}
}

func TestEncodeNilData(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Encode(rec, requestWithSpan(t), nil); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("data 为空时应省略字段：%s", rec.Body.String())
	}
}

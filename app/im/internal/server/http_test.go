package server_test

// 本文件用真实 SQLite + 真实 kratos server 跑一遍 HTTP 闭环：
// A 发消息 → B 拉历史 → B 标记已读 → A 在会话列表里看到 B 的已读位。
//
// 这就是交接文档验收 (b) 的自动化版本。生产库是 MySQL；
// 有 MySQL 环境时用 app/im/internal/data/repo_mysql_test.go 验证存储层语义。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/CycleZero/rym/api/im/v1"
	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/app/im/internal/data"
	"github.com/CycleZero/rym/app/im/internal/server"
	"github.com/CycleZero/rym/app/im/internal/service"
	"github.com/CycleZero/rym/pkg/common"
	"github.com/CycleZero/rym/pkg/response"
	"github.com/CycleZero/rym/pkg/tx"

	"github.com/go-kratos/kratos/v3/middleware"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

// envelope 对应 pkg/response 的统一信封。
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	TraceID string          `json:"trace_id"`
}

// testUserMiddleware 用 X-Test-User 头模拟已鉴权用户，
// 替代生产的 authMiddleware（这里只验证 IM 业务链路）。
func testUserMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			uid := 0
			if r, ok := khttp.RequestFromServerContext(ctx); ok {
				uid, _ = strconv.Atoi(r.Header.Get("X-Test-User"))
			}
			return next(common.WithMetadata(ctx, &common.RequestMetadata{UserID: uint(uid)}), req)
		}
	}
}

// newHTTPServer 起一个带统一信封的 kratos server（中间件/编码器与生产一致）。
func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	db := openSQLite(t)
	uc := biz.NewUsecase(data.NewRepo(db), tx.NewTxManager(db), biz.Settings{
		DefaultPageSize: 20, MaxPageSize: 100, MaxBodyBytes: 64 * 1024,
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := khttp.NewServer(
		khttp.Middleware(testUserMiddleware()),
		khttp.ResponseEncoder(response.Encode),
		khttp.ErrorEncoder(func(w http.ResponseWriter, r *http.Request, err error) {
			response.EncodeError(w, r, err, logger)
		}),
	)
	server.New(srv, service.NewImService(uc, logger))

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rym.db")
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=5000"), &gorm.Config{
		Logger: glogger.Default.LogMode(glogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败：%v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败：%v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	src, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_init_sqlite.sql"))
	if err != nil {
		t.Fatalf("读取迁移脚本失败：%v", err)
	}
	for _, stmt := range strings.Split(string(src), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("执行迁移失败：%v", err)
		}
	}
	return db
}

// doJSON 发一个带 X-Test-User 的 JSON 请求。
func doJSON(t *testing.T, base, method, path, userID string, payload any) (int, envelope) {
	t.Helper()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求失败：%v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", userID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	return resp.StatusCode, env
}

func decodeData[T any](t *testing.T, env envelope) T {
	t.Helper()
	var out T
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &out); err != nil {
			t.Fatalf("解析 data 失败：%v（原始：%s）", err, env.Data)
		}
	}
	return out
}

// TestHTTPClosedLoop 验收 (b)：A 发 → B 拉 → B 已读 → A 看到 B 的已读位。
func TestHTTPClosedLoop(t *testing.T) {
	ts := newHTTPServer(t)
	const a, b = "1", "2"

	// 1) A 给 B 发一条消息（单聊会话按双方 uid 自动创建）
	status, env := doJSON(t, ts.URL, http.MethodPost, "/v1/im/messages", a, map[string]any{
		"to_user_id":    2,
		"client_msg_id": "loop-1",
		"type":          1,
		"body":          `{"text":"hello"}`,
	})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("发送失败：status=%d code=%d message=%s", status, env.Code, env.Message)
	}
	sent := decodeData[v1.SendMessageResponse](t, env)
	if sent.GetMessage().GetSeq() != 1 {
		t.Fatalf("首条消息 seq=%d，期望 1", sent.GetMessage().GetSeq())
	}
	convID := sent.GetMessage().GetConversationId()

	// 2) 重发同一个 client_msg_id：返回同一条（幂等）
	status, env = doJSON(t, ts.URL, http.MethodPost, "/v1/im/messages", a, map[string]any{
		"to_user_id":    2,
		"client_msg_id": "loop-1",
		"type":          1,
		"body":          `{"text":"hello"}`,
	})
	dup := decodeData[v1.SendMessageResponse](t, env)
	if status != http.StatusOK || !dup.GetDuplicated() {
		t.Fatalf("重发未命中幂等：status=%d body=%s", status, env.Data)
	}

	// 3) B 拉历史
	status, env = doJSON(t, ts.URL, http.MethodGet,
		"/v1/im/messages?conversation_id="+strconv.FormatUint(convID, 10), b, nil)
	if status != http.StatusOK {
		t.Fatalf("B 拉历史失败：status=%d message=%s", status, env.Message)
	}
	pulled := decodeData[v1.PullMessagesResponse](t, env)
	if len(pulled.GetList()) != 1 || pulled.GetList()[0].GetSeq() != 1 {
		t.Fatalf("B 拉到的消息不对：%s", env.Data)
	}
	if pulled.GetMaxSeq() != 1 {
		t.Fatalf("max_seq=%d，期望 1", pulled.GetMaxSeq())
	}

	// 4) B 标记已读
	status, env = doJSON(t, ts.URL, http.MethodPost,
		"/v1/im/conversations/"+strconv.FormatUint(convID, 10)+"/read", b, map[string]any{"read_seq": 1})
	if status != http.StatusOK {
		t.Fatalf("B 标记已读失败：status=%d message=%s", status, env.Message)
	}
	if got := decodeData[v1.MarkReadResponse](t, env); got.GetReadSeq() != 1 || got.GetUnreadCount() != 0 {
		t.Fatalf("已读响应不对：read_seq=%d unread=%d", got.GetReadSeq(), got.GetUnreadCount())
	}

	// 5) A 在会话列表里看到 B 的已读位
	status, env = doJSON(t, ts.URL, http.MethodGet, "/v1/im/conversations", a, nil)
	if status != http.StatusOK {
		t.Fatalf("A 拉会话列表失败：status=%d message=%s", status, env.Message)
	}
	list := decodeData[v1.ListConversationsResponse](t, env)
	if len(list.GetList()) != 1 {
		t.Fatalf("A 应有 1 个会话，实际 %d", len(list.GetList()))
	}
	conv := list.GetList()[0]
	if conv.GetPeerReadSeq() != 1 {
		t.Fatalf("A 看到 B 的已读位=%d，期望 1", conv.GetPeerReadSeq())
	}
	if conv.GetLastMessage().GetSeq() != 1 {
		t.Fatalf("会话摘要缺少最后一条消息：%s", env.Data)
	}

	// 6) 非成员访问 → 403（且响应体不泄漏会话是否存在）
	status, env = doJSON(t, ts.URL, http.MethodGet,
		"/v1/im/messages?conversation_id="+strconv.FormatUint(convID, 10), "3", nil)
	if status != http.StatusForbidden || env.Code != 40300 {
		t.Fatalf("非成员访问应为 403/40300，实际 %d/%d", status, env.Code)
	}
}

// TestRoutesRegisteredFromProto 路由由 proto 生成，漏注册编译期不会报错，靠这里兜底。
func TestRoutesRegisteredFromProto(t *testing.T) {
	srv := khttp.NewServer()
	server.New(srv, fakeService{})

	var routes []string
	if err := srv.WalkRoute(func(r khttp.RouteInfo) error {
		routes = append(routes, r.Method+" "+r.Path)
		return nil
	}); err != nil {
		t.Fatalf("WalkRoute: %v", err)
	}

	want := []string{
		"POST /v1/im/messages",
		"GET /v1/im/messages",
		"GET /v1/im/conversations",
		"POST /v1/im/conversations/{conversation_id}/read",
	}
	for _, w := range want {
		found := false
		for _, got := range routes {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("缺少路由 %q，实际注册：%v", w, routes)
		}
	}
}

// fakeService 只用于路由注册测试。
type fakeService struct{}

func (fakeService) SendMessage(context.Context, *v1.SendMessageRequest) (*v1.SendMessageResponse, error) {
	return nil, nil
}
func (fakeService) PullMessages(context.Context, *v1.PullMessagesRequest) (*v1.PullMessagesResponse, error) {
	return nil, nil
}
func (fakeService) ListConversations(context.Context, *v1.ListConversationsRequest) (*v1.ListConversationsResponse, error) {
	return nil, nil
}
func (fakeService) MarkRead(context.Context, *v1.MarkReadRequest) (*v1.MarkReadResponse, error) {
	return nil, nil
}

package server_test

// 用户模块的 HTTP 闭环：真实 SQLite + 真实 kratos server + 生产中间件链
// （含 pkg/transport 的 JWT 校验），验证注册 → 登录 → 带令牌访问 /me。

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
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/CycleZero/rym/api/user/v1"
	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/app/user/internal/data"
	"github.com/CycleZero/rym/app/user/internal/server"
	"github.com/CycleZero/rym/app/user/internal/service"
	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/token"
	"github.com/CycleZero/rym/pkg/transport"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

const (
	testSecret   = "test-secret-please-change"
	testPassword = "password123"
)

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	TraceID string          `json:"trace_id"`
}

func newHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()

	path := filepath.Join(t.TempDir(), "user.db")
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

	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取迁移目录失败：%v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_sqlite.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取迁移脚本 %s 失败：%v", name, err)
		}
		for _, stmt := range strings.Split(string(src), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if err := db.Exec(stmt).Error; err != nil {
				t.Fatalf("执行迁移 %s 失败：%v", name, err)
			}
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc := biz.NewUsecase(data.NewRepo(db), biz.Settings{BcryptCost: bcrypt.MinCost})
	issuer, err := token.NewIssuer(testSecret, time.Hour)
	if err != nil {
		t.Fatalf("创建令牌签发器失败：%v", err)
	}

	// 进程级配置：只填本测试需要的字段，其余保持零值（getter 都是 nil-safe）。
	bc := &conf.Bootstrap{
		Server:   &conf.Server{Http: &conf.HTTP{Host: "127.0.0.1", Port: 8000}},
		Security: &conf.Security{JwtSecret: testSecret},
	}

	// 用生产同款中间件链与响应信封，避免测试只验证到 handler。
	srv := transport.NewHTTPServer(bc, transport.NewMiddlewares(bc, logger), logger, nil)
	server.New(srv, service.NewUserService(uc, issuer, logger))

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func doJSON(t *testing.T, base, method, path, bearer string, payload any) (int, envelope) {
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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

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

// TestHTTPRegisterLoginMe 验收：注册 → 登录 → 带令牌 /me。
func TestHTTPRegisterLoginMe(t *testing.T) {
	ts := newHTTPServer(t)

	// 1) 注册：用户名统一小写，密码永不下发
	status, env := doJSON(t, ts.URL, http.MethodPost, "/v1/users/register", "", map[string]any{
		"username": "Alice",
		"password": testPassword,
		"nickname": "爱丽丝",
	})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("注册失败：status=%d code=%d message=%s", status, env.Code, env.Message)
	}
	if strings.Contains(string(env.Data), "password") {
		t.Fatalf("注册响应泄漏了密码字段：%s", env.Data)
	}
	registered := decodeData[v1.User](t, env)
	if registered.GetId() == 0 || registered.GetUsername() != "alice" || registered.GetNickname() != "爱丽丝" {
		t.Fatalf("注册返回的用户不对：%s", env.Data)
	}

	// 2) 重复注册 → 409
	status, env = doJSON(t, ts.URL, http.MethodPost, "/v1/users/register", "", map[string]any{
		"username": "alice",
		"password": testPassword,
	})
	if status != http.StatusConflict || env.Code != 40900 {
		t.Fatalf("重复注册应为 409/40900，实际 %d/%d", status, env.Code)
	}

	// 3) 登录 → 拿到令牌
	status, env = doJSON(t, ts.URL, http.MethodPost, "/v1/users/login", "", map[string]any{
		"username": "alice",
		"password": testPassword,
	})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("登录失败：status=%d code=%d message=%s", status, env.Code, env.Message)
	}
	login := decodeData[v1.LoginResponse](t, env)
	if login.GetAccessToken() == "" || login.GetTokenType() != "Bearer" {
		t.Fatalf("登录响应缺少令牌：%s", env.Data)
	}
	if login.GetExpiresAt() <= time.Now().Unix() {
		t.Fatalf("令牌过期时间应在未来：%d", login.GetExpiresAt())
	}

	// 4) 带令牌访问 /me：由生产 authMiddleware 解析 user_id
	status, env = doJSON(t, ts.URL, http.MethodGet, "/v1/users/me", login.GetAccessToken(), nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("带令牌 /me 失败：status=%d code=%d message=%s", status, env.Code, env.Message)
	}
	me := decodeData[v1.User](t, env)
	if me.GetId() != registered.GetId() || me.GetUsername() != "alice" {
		t.Fatalf("/me 返回的用户不对：%s", env.Data)
	}

	// 5) 不带令牌 /me → 401
	status, env = doJSON(t, ts.URL, http.MethodGet, "/v1/users/me", "", nil)
	if status != http.StatusUnauthorized || env.Code != 40100 {
		t.Fatalf("匿名 /me 应为 401/40100，实际 %d/%d", status, env.Code)
	}

	// 6) 伪造令牌 → 401（可选鉴权模式按匿名处理）
	status, env = doJSON(t, ts.URL, http.MethodGet, "/v1/users/me", "not-a-jwt", nil)
	if status != http.StatusUnauthorized || env.Code != 40100 {
		t.Fatalf("伪造令牌 /me 应为 401/40100，实际 %d/%d", status, env.Code)
	}

	// 7) 密码错误 → 401，消息不泄漏账号是否存在
	status, env = doJSON(t, ts.URL, http.MethodPost, "/v1/users/login", "", map[string]any{
		"username": "alice",
		"password": "wrong-password",
	})
	if status != http.StatusUnauthorized || env.Code != 40100 {
		t.Fatalf("密码错误应为 401/40100，实际 %d/%d", status, env.Code)
	}
	if env.Message != "用户名或密码错误" {
		t.Fatalf("错误消息不合规：%q", env.Message)
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
		"POST /v1/users/register",
		"POST /v1/users/login",
		"GET /v1/users/me",
		"GET /v1/users/search",
		"GET /v1/users/{id}",
		"POST /v1/users/batch",
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

type fakeService struct{}

func (fakeService) Register(ctx context.Context, req *v1.RegisterRequest) (*v1.User, error) {
	return nil, nil
}
func (fakeService) Login(ctx context.Context, req *v1.LoginRequest) (*v1.LoginResponse, error) {
	return nil, nil
}
func (fakeService) GetMe(ctx context.Context, req *v1.GetMeRequest) (*v1.User, error) {
	return nil, nil
}
func (fakeService) GetUser(ctx context.Context, req *v1.GetUserRequest) (*v1.User, error) {
	return nil, nil
}
func (fakeService) BatchGetUsers(ctx context.Context, req *v1.BatchGetUsersRequest) (*v1.BatchGetUsersResponse, error) {
	return nil, nil
}
func (fakeService) SearchUsers(ctx context.Context, req *v1.SearchUsersRequest) (*v1.SearchUsersResponse, error) {
	return nil, nil
}

package data_test

// 用户模块的数据层测试：跑在真实 SQLite 上（app/user/migrations/001_init_sqlite.sql）。
//
// 生产库是 MySQL：这里的 SQL 都是标准语法，唯一需要留意的方言差异
// （唯一键冲突错误码）已经在 repo.Create 里用"回查"规避。
// MySQL 真库冒烟测试见 repo_mysql_test.go（有 DSN 时才跑）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/app/user/internal/data"
	"github.com/CycleZero/rym/pkg/errs"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

const testPassword = "password123"

func newUsecase(t *testing.T) (*biz.Usecase, *gorm.DB) {
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

	applyMigration(t, db)

	uc := biz.NewUsecase(data.NewRepo(db), biz.Settings{BcryptCost: bcrypt.MinCost})
	return uc, db
}

func applyMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_init_sqlite.sql"))
	if err != nil {
		t.Fatalf("读取迁移脚本失败：%v", err)
	}
	for _, stmt := range strings.Split(string(src), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("执行迁移失败：%v\nSQL: %s", err, stmt)
		}
	}
}

func register(t *testing.T, uc *biz.Usecase, username, password, nickname string) *biz.User {
	t.Helper()
	user, err := uc.Register(context.Background(), biz.RegisterInput{
		Username: username, Password: password, Nickname: nickname,
	})
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	return user
}

// TestRegisterAndLogin 覆盖注册 → 登录 → 取用户的主路径。
func TestRegisterAndLogin(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	user := register(t, uc, "Alice", testPassword, "")
	if user.ID == 0 {
		t.Fatal("注册后应回填用户 ID")
	}
	if user.Username != "alice" {
		t.Fatalf("用户名应统一小写，实际 %q", user.Username)
	}
	if user.Nickname != "alice" {
		t.Fatalf("昵称为空时应回退为用户名，实际 %q", user.Nickname)
	}
	if user.PasswordHash == testPassword {
		t.Fatal("密码不能明文存储")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(testPassword)); err != nil {
		t.Fatalf("密码哈希无法校验：%v", err)
	}

	// 登录大小写不敏感（用户名已被归一化）
	logged, err := uc.Login(ctx, biz.LoginInput{Username: "ALICE", Password: testPassword})
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if logged.ID != user.ID {
		t.Fatalf("登录返回的用户不对：%d != %d", logged.ID, user.ID)
	}

	got, err := uc.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("按 ID 查询失败：%v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("按 ID 查到的用户不对：%+v", got)
	}
}

// TestRegisterDuplicate 唯一索引冲突翻译成 409，且并发下也只有一个成功。
func TestRegisterDuplicate(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	register(t, uc, "alice", testPassword, "")

	_, err := uc.Register(ctx, biz.RegisterInput{Username: "Alice", Password: testPassword})
	if !errors.Is(err, biz.ErrUsernameTaken) {
		t.Fatalf("期望 ErrUsernameTaken，实际 %v", err)
	}
	status, code, _ := errs.Resolve(err)
	if status != 409 || code != errs.CodeConflict {
		t.Fatalf("期望 409/%d，实际 %d/%d", errs.CodeConflict, status, code)
	}

	// 并发抢注同名：恰好一个成功，另一个必须拿到冲突错误。
	const n = 8
	var wg sync.WaitGroup
	success := make(chan uint64, n)
	conflicts := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := uc.Register(ctx, biz.RegisterInput{Username: "bob", Password: testPassword})
			if err != nil {
				conflicts <- err
				return
			}
			success <- u.ID
		}()
	}
	wg.Wait()
	close(success)
	close(conflicts)

	successCount := 0
	for range success {
		successCount++
	}
	if successCount != 1 {
		t.Fatalf("并发抢注应只有 1 个成功，实际 %d 个", successCount)
	}
	for err := range conflicts {
		if !errors.Is(err, biz.ErrUsernameTaken) {
			t.Fatalf("并发冲突应是 ErrUsernameTaken，实际 %v", err)
		}
	}
}

// TestLoginFailures 账号枚举防护：未知用户与密码错误返回同一个错误。
func TestLoginFailures(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()
	register(t, uc, "alice", testPassword, "")

	wrong, err := uc.Login(ctx, biz.LoginInput{Username: "alice", Password: "wrong-password"})
	if wrong != nil || !errors.Is(err, biz.ErrInvalidCredentials) {
		t.Fatalf("密码错误应返回 ErrInvalidCredentials，实际 %v", err)
	}

	unknown, err := uc.Login(ctx, biz.LoginInput{Username: "nobody", Password: testPassword})
	if unknown != nil || !errors.Is(err, biz.ErrInvalidCredentials) {
		t.Fatalf("未知用户应返回 ErrInvalidCredentials，实际 %v", err)
	}

	status, code, msg := errs.Resolve(err)
	if status != 401 || code != errs.CodeUnauthorized {
		t.Fatalf("期望 401/%d，实际 %d/%d", errs.CodeUnauthorized, status, code)
	}
	if msg != "用户名或密码错误" {
		t.Fatalf("错误消息不应泄漏账号是否存在，实际 %q", msg)
	}
}

// TestDisabledUser 禁用账号：密码正确才提示禁用，否则仍是"用户名或密码错误"。
func TestDisabledUser(t *testing.T) {
	uc, db := newUsecase(t)
	ctx := context.Background()
	user := register(t, uc, "alice", testPassword, "")

	if err := db.Model(&data.UserPO{}).Where("id = ?", user.ID).Update("status", biz.UserStatusDisabled).Error; err != nil {
		t.Fatalf("禁用账号失败：%v", err)
	}

	if _, err := uc.Login(ctx, biz.LoginInput{Username: "alice", Password: "wrong-password"}); !errors.Is(err, biz.ErrInvalidCredentials) {
		t.Fatalf("禁用账号密码错误时仍应返回 ErrInvalidCredentials，实际 %v", err)
	}
	if _, err := uc.Login(ctx, biz.LoginInput{Username: "alice", Password: testPassword}); !errors.Is(err, biz.ErrAccountDisabled) {
		t.Fatalf("禁用账号密码正确时应返回 ErrAccountDisabled，实际 %v", err)
	}
	if _, err := uc.GetByID(ctx, user.ID); !errors.Is(err, biz.ErrAccountDisabled) {
		t.Fatalf("禁用账号 GetByID 应返回 ErrAccountDisabled，实际 %v", err)
	}
}

// TestRegisterValidation 入参校验：失败必须是 400，且不触达数据库。
func TestRegisterValidation(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   biz.RegisterInput
	}{
		{"用户名太短", biz.RegisterInput{Username: "ab", Password: testPassword}},
		{"用户名含非法字符", biz.RegisterInput{Username: "alice!", Password: testPassword}},
		{"用户名过长", biz.RegisterInput{Username: strings.Repeat("a", 33), Password: testPassword}},
		{"密码太短", biz.RegisterInput{Username: "alice", Password: "1234567"}},
		{"密码超过 bcrypt 上限", biz.RegisterInput{Username: "alice", Password: strings.Repeat("a", 73)}},
		{"昵称过长", biz.RegisterInput{Username: "alice", Password: testPassword, Nickname: strings.Repeat("昵", 33)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Register(ctx, tc.in)
			if !errors.Is(err, errs.InvalidArgument("x")) {
				t.Fatalf("期望 400 InvalidArgument，实际 %v", err)
			}
			status, _, _ := errs.Resolve(err)
			if status != 400 {
				t.Fatalf("期望 HTTP 400，实际 %d", status)
			}
		})
	}
}

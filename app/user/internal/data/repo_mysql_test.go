package data_test

// 用户模块的 MySQL 真库冒烟：只有提供 RYM_TEST_MYSQL_DSN 时才运行。
//
// 用户模块的 SQL 比 IM 简单（单条 INSERT / 按唯一索引查询），
// 这里主要验证：唯一键冲突能被翻译成 ErrUsernameTaken、登录读回的数据完整。
//
// 用法：
//   RYM_TEST_MYSQL_DSN='root:pass@tcp(127.0.0.1:3306)/rym_test?charset=utf8mb4&parseTime=True&loc=Local' \
//     go test ./app/user/internal/data -run TestMySQLUserAcceptance -v

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/app/user/internal/data"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

func TestMySQLUserAcceptance(t *testing.T) {
	dsn := os.Getenv("RYM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("设置 RYM_TEST_MYSQL_DSN 后运行（生产库是 MySQL，默认不连任何数据库）")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: glogger.Default.LogMode(glogger.Silent),
	})
	if err != nil {
		t.Fatalf("连接 MySQL 失败：%v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败：%v", err)
	}
	sqlDB.SetMaxOpenConns(5)
	t.Cleanup(func() { _ = sqlDB.Close() })

	applyMySQLMigrations(t, db)
	if err := db.Exec("DELETE FROM `users`").Error; err != nil {
		t.Fatalf("清空 users 失败：%v", err)
	}

	uc := biz.NewUsecase(data.NewRepo(db), biz.Settings{BcryptCost: bcrypt.MinCost})
	ctx := context.Background()

	user, err := uc.Register(ctx, biz.RegisterInput{Username: "MySQL_Alice", Password: testPassword})
	if err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	if user.Username != "mysql_alice" {
		t.Fatalf("用户名应统一小写，实际 %q", user.Username)
	}

	if _, err := uc.Register(ctx, biz.RegisterInput{Username: "mysql_alice", Password: testPassword}); !errors.Is(err, biz.ErrUsernameTaken) {
		t.Fatalf("重复注册应返回 ErrUsernameTaken，实际 %v", err)
	}

	logged, err := uc.Login(ctx, biz.LoginInput{Username: "MYSQL_ALICE", Password: testPassword})
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if logged.ID != user.ID {
		t.Fatalf("登录用户 id 不一致：%d != %d", logged.ID, user.ID)
	}

	if _, err := uc.Login(ctx, biz.LoginInput{Username: "mysql_alice", Password: "wrong-password"}); !errors.Is(err, biz.ErrInvalidCredentials) {
		t.Fatalf("密码错误应返回 ErrInvalidCredentials，实际 %v", err)
	}
}

func applyMySQLMigrations(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (name VARCHAR(255) NOT NULL, applied_at DATETIME NOT NULL, PRIMARY KEY (name)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error; err != nil {
		t.Fatalf("创建 schema_migrations 失败：%v", err)
	}

	dir := "../../migrations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 MySQL 迁移目录失败：%v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_mysql.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		// 与 make migrate 使用同一个相对路径作为迁移标识。
		key := "app/user/migrations/" + name
		var applied int64
		if err := db.Raw("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", key).Scan(&applied).Error; err != nil {
			t.Fatalf("查询迁移记录失败：%v", err)
		}
		if applied > 0 {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 MySQL 迁移 %s 失败：%v", name, err)
		}
		for _, stmt := range strings.Split(string(src), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if err := db.Exec(stmt).Error; err != nil {
				t.Fatalf("执行 MySQL 迁移 %s 失败：%v\nSQL: %s", name, err, stmt)
			}
		}
		if err := db.Exec("INSERT INTO schema_migrations(name, applied_at) VALUES (?, NOW())", key).Error; err != nil {
			t.Fatalf("记录迁移 %s 失败：%v", name, err)
		}
	}
}

package data_test

// MySQL 真库验收：只有显式提供 RYM_TEST_MYSQL_DSN 时才运行，默认 skip。
//
// 为什么单独留这一份：SQLite 测试串行化写入，验证不了 MySQL 的行锁/快照语义。
// 这里用真实 MySQL 跑「并发发号」和「幂等重发」，验证 data.rowLock 的 FOR UPDATE
// 分支确实把同一会话的写者排了队。
//
// 用法：
//   RYM_TEST_MYSQL_DSN='root:pass@tcp(127.0.0.1:3306)/rym_test?charset=utf8mb4&parseTime=True&loc=Local' \
//     go test ./app/im/internal/data -run TestMySQLAcceptance -v

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/app/im/internal/data"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

func TestMySQLAcceptance(t *testing.T) {
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
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	t.Cleanup(func() { _ = sqlDB.Close() })

	applyMySQLMigration(t, db)
	// 只清空 IM 三张表（测试库专用，别指向生产库）。
	for _, table := range []string{"messages", "conversation_participants", "conversations"} {
		if err := db.Exec("DELETE FROM `" + table + "`").Error; err != nil {
			t.Fatalf("清空 %s 失败：%v", table, err)
		}
	}

	uc := biz.NewUsecase(data.NewRepo(db), tx.NewTxManager(db), staticSettings{biz.Settings{
		DefaultPageSize: 20, MaxPageSize: 100, MaxBodyBytes: 64 * 1024,
	}}, nil)
	ctx := context.Background()

	t.Run("幂等重发", func(t *testing.T) {
		const a, b = uint64(1001), uint64(1002)
		in := biz.SendMessageInput{
			ToUserID: b, SenderID: a, ClientMsgID: "mysql-idem",
			Type: biz.MessageTypeText, Body: testBody,
		}
		first := mustSend(t, uc, in)
		for i := 0; i < 20; i++ {
			got := mustSend(t, uc, in)
			if !got.Duplicated || got.Message.Seq != first.Message.Seq {
				t.Fatalf("重发未幂等：duplicated=%v seq=%d want=%d",
					got.Duplicated, got.Message.Seq, first.Message.Seq)
			}
		}
		var count int64
		if err := db.Model(&data.MessagePO{}).
			Where("conversation_id = ?", first.Message.ConversationID).Count(&count).Error; err != nil {
			t.Fatalf("统计消息数失败：%v", err)
		}
		if count != 1 {
			t.Fatalf("重发 21 次应只有 1 行，实际 %d 行", count)
		}
	})

	t.Run("并发发号连续", func(t *testing.T) {
		const a, b = uint64(1003), uint64(1004)
		seed := mustSend(t, uc, biz.SendMessageInput{
			ToUserID: b, SenderID: a, ClientMsgID: "mysql-seed",
			Type: biz.MessageTypeText, Body: testBody,
		})
		convID := seed.Message.ConversationID

		const n = 50
		seqs := make([]int64, n)
		errsCh := make(chan error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				res, err := uc.SendMessage(ctx, biz.SendMessageInput{
					ConversationID: convID, SenderID: a,
					ClientMsgID: fmt.Sprintf("mysql-concurrent-%d", i),
					Type:        biz.MessageTypeText, Body: testBody,
				})
				if err != nil {
					errsCh <- err
					return
				}
				seqs[i] = res.Message.Seq
			}(i)
		}
		wg.Wait()
		close(errsCh)
		for err := range errsCh {
			t.Fatalf("并发发送失败：%v", err)
		}

		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		for i, seq := range seqs {
			if want := int64(i + 2); seq != want {
				t.Fatalf("seq 不连续：第 %d 个是 %d，期望 %d（%v）", i, seq, want, seqs)
			}
		}
	})

	t.Run("已读只增不减与未读", func(t *testing.T) {
		const a, b = uint64(1005), uint64(1006)
		seed := mustSend(t, uc, biz.SendMessageInput{
			ToUserID: b, SenderID: a, ClientMsgID: "mysql-unread-0",
			Type: biz.MessageTypeText, Body: testBody,
		})
		convID := seed.Message.ConversationID
		for i := 1; i < 3; i++ {
			mustSend(t, uc, biz.SendMessageInput{
				ConversationID: convID, SenderID: a,
				ClientMsgID: fmt.Sprintf("mysql-unread-%d", i),
				Type:        biz.MessageTypeText, Body: testBody,
			})
		}

		first, err := uc.MarkRead(ctx, biz.MarkReadInput{ConversationID: convID, UserID: b, ReadSeq: 3})
		if err != nil {
			t.Fatalf("已读上报失败：%v", err)
		}
		if first.Unread != 0 {
			t.Fatalf("读完未读应为 0，实际 %d", first.Unread)
		}
		second, err := uc.MarkRead(ctx, biz.MarkReadInput{ConversationID: convID, UserID: b, ReadSeq: 1})
		if err != nil {
			t.Fatalf("乱序已读上报失败：%v", err)
		}
		if second.ReadSeq != 3 {
			t.Fatalf("已读位回退到 %d，期望 3", second.ReadSeq)
		}
	})
}

func applyMySQLMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	src, err := os.ReadFile("../../migrations/001_init_mysql.sql")
	if err != nil {
		t.Fatalf("读取 MySQL 迁移失败：%v", err)
	}
	for _, stmt := range strings.Split(string(src), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("执行 MySQL 迁移失败：%v\nSQL: %s", err, stmt)
		}
	}
}

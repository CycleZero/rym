package data_test

// 本文件的测试跑在真实 SQLite 上（app/im/migrations/001_init_sqlite.sql），
// 覆盖文档 §16.5 的验收标准。生产库是 MySQL：数据层只用了两边都支持的标准 SQL，
// 唯一的方言分支是 MySQL 的 SELECT ... FOR UPDATE（见 data.rowLock）。
//
// 为什么不用 fake repo：seq 连续性、唯一索引幂等、游标分页这些性质
// 恰恰由数据库保证，用 fake 只能测到"假实现自己也对"。
// SQLite 测试用单连接（SetMaxOpenConns(1)）串行化写入，模拟 MySQL 行锁的排队语义；
// MySQL 真库的并发验证见 repo_mysql_test.go（有 DSN 时才跑）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/app/im/internal/data"
	"github.com/CycleZero/rym/pkg/errs"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

const testBody = `{"text":"hello"}`

// staticSettings 是测试用的固定配置快照。
type staticSettings struct{ s biz.Settings }

func (s staticSettings) Settings() biz.Settings { return s.s }

// newUsecase 建一个跑在独立 SQLite 文件上的完整业务栈（biz + data + 真实事务）。
func newUsecase(t *testing.T) (*biz.Usecase, *gorm.DB) {
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
	// SQLite 只允许一个写者；单连接让并发测试的排队语义确定。
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	applyMigration(t, db)

	uc := biz.NewUsecase(data.NewRepo(db), tx.NewTxManager(db), staticSettings{biz.Settings{
		DefaultPageSize: 20,
		MaxPageSize:     100,
		MaxBodyBytes:    64 * 1024,
	}})
	return uc, db
}

// applyMigration 执行 SQLite 建表脚本（按分号切分，够用且不引入迁移框架）。
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

func mustSend(t *testing.T, uc *biz.Usecase, in biz.SendMessageInput) *biz.SendMessageResult {
	t.Helper()
	res, err := uc.SendMessage(context.Background(), in)
	if err != nil {
		t.Fatalf("发送消息失败：%v", err)
	}
	return res
}

// TestIdempotentResend 验收 1：同一 client_msg_id 重发 100 次 → 1 行消息、同一个 seq。
func TestIdempotentResend(t *testing.T) {
	uc, db := newUsecase(t)
	in := biz.SendMessageInput{
		ToUserID:    2,
		SenderID:    1,
		ClientMsgID: "idem-1",
		Type:        biz.MessageTypeText,
		Body:        testBody,
	}

	first := mustSend(t, uc, in)
	for i := 0; i < 99; i++ {
		got := mustSend(t, uc, in)
		if !got.Duplicated {
			t.Fatalf("第 %d 次重发未命中幂等键", i+2)
		}
		if got.Message.Seq != first.Message.Seq {
			t.Fatalf("重发返回了不同的 seq：first=%d got=%d", first.Message.Seq, got.Message.Seq)
		}
		if got.Message.MsgID != first.Message.MsgID {
			t.Fatalf("重发返回了不同的 msg_id：first=%s got=%s", first.Message.MsgID, got.Message.MsgID)
		}
	}

	var count int64
	if err := db.Model(&data.MessagePO{}).Where("conversation_id = ?", first.Message.ConversationID).Count(&count).Error; err != nil {
		t.Fatalf("统计消息数失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("重发 100 次应只有 1 行消息，实际 %d 行", count)
	}
}

// TestConcurrentSendKeepsSeqContiguous 验收 2：并发 100 条 → seq 连续、无空洞、无重复。
func TestConcurrentSendKeepsSeqContiguous(t *testing.T) {
	uc, _ := newUsecase(t)

	// 先建会话并占用 seq=1，接下来并发 100 条应当是 2..101。
	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID

	const n = 100
	seqs := make([]int64, n)
	errsCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := uc.SendMessage(context.Background(), biz.SendMessageInput{
				ConversationID: convID,
				SenderID:       1,
				ClientMsgID:    fmt.Sprintf("concurrent-%d", i),
				Type:           biz.MessageTypeText,
				Body:           testBody,
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
		want := int64(i + 2)
		if seq != want {
			t.Fatalf("seq 不连续：第 %d 个是 %d，期望 %d（完整序列：%v）", i, seq, want, seqs)
		}
	}
}

// TestPullMessagesCursorPagination 验收 3：按 seq< 翻 10 页 → 无重复、无遗漏。
func TestPullMessagesCursorPagination(t *testing.T) {
	uc, _ := newUsecase(t)

	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID
	const total = 25
	for i := 1; i < total; i++ {
		mustSend(t, uc, biz.SendMessageInput{
			ConversationID: convID,
			SenderID:       1,
			ClientMsgID:    fmt.Sprintf("page-%d", i),
			Type:           biz.MessageTypeText,
			Body:           testBody,
		})
	}

	before := int64(0)
	var got []int64
	seen := make(map[int64]bool, total)
	for page := 0; page < 10; page++ {
		res, err := uc.PullMessages(context.Background(), biz.PullMessagesInput{
			ConversationID: convID,
			UserID:         1,
			BeforeSeq:      before,
			Limit:          3,
		})
		if err != nil {
			t.Fatalf("第 %d 页拉取失败：%v", page+1, err)
		}
		if len(res.List) == 0 {
			break
		}
		for i, msg := range res.List {
			if i > 0 && msg.Seq <= res.List[i-1].Seq {
				t.Fatalf("单页内 seq 未升序：%v", seqSlice(res.List))
			}
			if seen[msg.Seq] {
				t.Fatalf("翻页出现重复 seq=%d", msg.Seq)
			}
			seen[msg.Seq] = true
			got = append(got, msg.Seq)
		}
		before = res.List[0].Seq // 本页最老的一条，作为下一页的游标
		if !res.HasMore {
			break
		}
	}

	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != total {
		t.Fatalf("翻页拿到 %d 条，期望 %d 条：%v", len(got), total, got)
	}
	for i, seq := range got {
		if seq != int64(i+1) {
			t.Fatalf("翻页结果不连续：第 %d 个是 %d（完整序列：%v）", i, seq, got)
		}
	}
}

// TestPullMessagesAfterSeq 补洞：after_seq 返回缺口后的升序消息。
func TestPullMessagesAfterSeq(t *testing.T) {
	uc, _ := newUsecase(t)
	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID
	for i := 0; i < 4; i++ {
		mustSend(t, uc, biz.SendMessageInput{
			ConversationID: convID, SenderID: 1, ClientMsgID: fmt.Sprintf("gap-%d", i),
			Type: biz.MessageTypeText, Body: testBody,
		})
	}

	res, err := uc.PullMessages(context.Background(), biz.PullMessagesInput{
		ConversationID: convID, UserID: 1, AfterSeq: 2, Limit: 10,
	})
	if err != nil {
		t.Fatalf("补洞拉取失败：%v", err)
	}
	want := []int64{3, 4, 5}
	if len(res.List) != len(want) {
		t.Fatalf("补洞返回 %d 条，期望 %d 条", len(res.List), len(want))
	}
	for i := range want {
		if res.List[i].Seq != want[i] {
			t.Fatalf("补洞第 %d 条 seq=%d，期望 %d", i, res.List[i].Seq, want[i])
		}
	}
	if res.MaxSeq != 5 || res.HasMore {
		t.Fatalf("max_seq=%d has_more=%v，期望 5/false", res.MaxSeq, res.HasMore)
	}
}

// TestMarkReadIsMonotonic 验收 4：乱序上报（先 100 再 90）→ read_seq 仍为 100。
func TestMarkReadIsMonotonic(t *testing.T) {
	uc, _ := newUsecase(t)

	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID
	for i := 0; i < 99; i++ {
		mustSend(t, uc, biz.SendMessageInput{
			ConversationID: convID, SenderID: 1, ClientMsgID: fmt.Sprintf("mono-%d", i),
			Type: biz.MessageTypeText, Body: testBody,
		})
	}

	first, err := uc.MarkRead(context.Background(), biz.MarkReadInput{
		ConversationID: convID, UserID: 2, ReadSeq: 100,
	})
	if err != nil {
		t.Fatalf("已读上报失败：%v", err)
	}
	if first.ReadSeq != 100 {
		t.Fatalf("read_seq=%d，期望 100", first.ReadSeq)
	}

	second, err := uc.MarkRead(context.Background(), biz.MarkReadInput{
		ConversationID: convID, UserID: 2, ReadSeq: 90,
	})
	if err != nil {
		t.Fatalf("乱序已读上报失败：%v", err)
	}
	if second.ReadSeq != 100 {
		t.Fatalf("已读位被回退到 %d，期望仍为 100", second.ReadSeq)
	}
	if second.Unread != 0 {
		t.Fatalf("已读到 100 后未读数应为 0，实际 %d", second.Unread)
	}
}

// TestUnreadExcludesOwnAndSystemMessages 验收 5（v1 简化版）：
// 未读数不计自己发的消息，也不计系统消息。
//
// 撤回（status 变更后扣回未读）属于 v1 明确不做的范围（文档 §16.2），
// 消息表已预留 status / version / edited_at，接入时在这里补一个用例即可。
func TestUnreadExcludesOwnAndSystemMessages(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	// A → B 三条普通消息
	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "u-0", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID
	for i := 1; i < 3; i++ {
		mustSend(t, uc, biz.SendMessageInput{
			ConversationID: convID, SenderID: 1, ClientMsgID: fmt.Sprintf("u-%d", i),
			Type: biz.MessageTypeText, Body: testBody,
		})
	}

	unreadOf := func(userID uint64) int64 {
		t.Helper()
		res, err := uc.ListConversations(ctx, biz.ListConversationsInput{UserID: userID})
		if err != nil {
			t.Fatalf("拉会话列表失败：%v", err)
		}
		if len(res.List) != 1 {
			t.Fatalf("用户 %d 应有 1 个会话，实际 %d", userID, len(res.List))
		}
		return res.List[0].Unread
	}

	if got := unreadOf(2); got != 3 {
		t.Fatalf("B 的未读数=%d，期望 3", got)
	}
	if got := unreadOf(1); got != 0 {
		t.Fatalf("A 自己发的消息不应计未读，实际 %d", got)
	}

	// B 读到第 2 条 → 剩 1 条未读
	mr, err := uc.MarkRead(ctx, biz.MarkReadInput{ConversationID: convID, UserID: 2, ReadSeq: 2})
	if err != nil {
		t.Fatalf("已读上报失败：%v", err)
	}
	if mr.Unread != 1 {
		t.Fatalf("读到 seq=2 后未读=%d，期望 1", mr.Unread)
	}

	// 系统消息：v1 不落消息表；这里用内部类型验证"即使落了也不计未读"。
	sys := mustSend(t, uc, biz.SendMessageInput{
		ConversationID: convID, SenderID: 1, ClientMsgID: "sys-1",
		Type: biz.MessageTypeSystem, Body: `{"text":"系统通知"}`,
	})
	if sys.Message.Seq != 4 {
		t.Fatalf("系统消息应占用 seq=4，实际 %d", sys.Message.Seq)
	}
	if got := unreadOf(2); got != 1 {
		t.Fatalf("系统消息不应计未读，B 未读=%d，期望 1", got)
	}

	// 自己发的也不计：B 再发一条，B 的未读仍为 1。
	mustSend(t, uc, biz.SendMessageInput{
		ConversationID: convID, SenderID: 2, ClientMsgID: "b-1",
		Type: biz.MessageTypeText, Body: testBody,
	})
	if got := unreadOf(2); got != 1 {
		t.Fatalf("B 自己发的消息不应计未读，实际 %d", got)
	}
}

// TestNonMemberForbidden 验收 6：非成员访问 → 403，且不泄漏会话是否存在。
func TestNonMemberForbidden(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID

	cases := []struct {
		name string
		call func() error
	}{
		{"发消息", func() error {
			_, err := uc.SendMessage(ctx, biz.SendMessageInput{
				ConversationID: convID, SenderID: 3, ClientMsgID: "c-1",
				Type: biz.MessageTypeText, Body: testBody,
			})
			return err
		}},
		{"拉消息", func() error {
			_, err := uc.PullMessages(ctx, biz.PullMessagesInput{
				ConversationID: convID, UserID: 3,
			})
			return err
		}},
		{"已读上报", func() error {
			_, err := uc.MarkRead(ctx, biz.MarkReadInput{
				ConversationID: convID, UserID: 3, ReadSeq: 1,
			})
			return err
		}},
		{"不存在的会话", func() error {
			_, err := uc.PullMessages(ctx, biz.PullMessagesInput{
				ConversationID: 999999, UserID: 1,
			})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, biz.ErrNotMember) {
				t.Fatalf("期望 ErrNotMember，实际 %v", err)
			}
			status, code, _ := errs.Resolve(err)
			if status != 403 || code != errs.CodeForbidden {
				t.Fatalf("期望 403/%d，实际 %d/%d", errs.CodeForbidden, status, code)
			}
		})
	}
}

// TestListConversationsShowsPeerReadSeq 会话列表里 A 能看到 B 的已读位。
func TestListConversationsShowsPeerReadSeq(t *testing.T) {
	uc, _ := newUsecase(t)
	ctx := context.Background()

	seed := mustSend(t, uc, biz.SendMessageInput{
		ToUserID: 2, SenderID: 1, ClientMsgID: "seed", Type: biz.MessageTypeText, Body: testBody,
	})
	convID := seed.Message.ConversationID

	if _, err := uc.MarkRead(ctx, biz.MarkReadInput{ConversationID: convID, UserID: 2, ReadSeq: 1}); err != nil {
		t.Fatalf("B 上报已读失败：%v", err)
	}

	res, err := uc.ListConversations(ctx, biz.ListConversationsInput{UserID: 1})
	if err != nil {
		t.Fatalf("A 拉会话列表失败：%v", err)
	}
	if len(res.List) != 1 {
		t.Fatalf("A 应有 1 个会话，实际 %d", len(res.List))
	}
	if got := res.List[0].PeerReadSeq; got != 1 {
		t.Fatalf("A 看到 B 的已读位=%d，期望 1", got)
	}
	if got := res.List[0].Unread; got != 0 {
		t.Fatalf("A 自己的消息不应计未读，实际 %d", got)
	}
	if res.List[0].LastMessage == nil || res.List[0].LastMessage.Seq != 1 {
		t.Fatalf("会话列表缺少最后一条消息摘要：%+v", res.List[0].LastMessage)
	}
}

func seqSlice(list []*biz.Message) []int64 {
	out := make([]int64, 0, len(list))
	for _, m := range list {
		out = append(out, m.Seq)
	}
	return out
}

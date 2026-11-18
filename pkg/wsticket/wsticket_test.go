package wsticket_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CycleZero/rym/pkg/cache"
	"github.com/CycleZero/rym/pkg/wsticket"
)

func TestIssueAndRedeemOnce(t *testing.T) {
	store := wsticket.NewStore(cache.NewMemoryCache())
	ctx := context.Background()

	ticket, expiresAt, err := store.Issue(ctx, 42, "tauri://localhost")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ticket == "" || expiresAt.IsZero() {
		t.Fatal("票据或过期时间不能为空")
	}

	uid, err := store.Redeem(ctx, ticket, "tauri://localhost")
	if err != nil || uid != 42 {
		t.Fatalf("Redeem = %d, %v；期望 42, nil", uid, err)
	}

	// 单次票据：第二次必须失败。
	if _, err := store.Redeem(ctx, ticket, "tauri://localhost"); !errors.Is(err, wsticket.ErrInvalid) {
		t.Fatalf("重复兑换应返回 ErrInvalid，实际 %v", err)
	}
}

func TestRedeemOriginMismatch(t *testing.T) {
	store := wsticket.NewStore(cache.NewMemoryCache())
	ctx := context.Background()

	ticket, _, err := store.Issue(ctx, 7, "https://app.example.com")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := store.Redeem(ctx, ticket, "https://evil.example.com"); !errors.Is(err, wsticket.ErrInvalid) {
		t.Fatalf("Origin 不匹配应返回 ErrInvalid，实际 %v", err)
	}
}

func TestRedeemInvalid(t *testing.T) {
	store := wsticket.NewStore(cache.NewMemoryCache())
	if _, err := store.Redeem(context.Background(), "", ""); !errors.Is(err, wsticket.ErrInvalid) {
		t.Fatalf("空票据应返回 ErrInvalid，实际 %v", err)
	}
	if _, err := store.Redeem(context.Background(), "nope", ""); !errors.Is(err, wsticket.ErrInvalid) {
		t.Fatalf("未知票据应返回 ErrInvalid，实际 %v", err)
	}
}

func TestNilCacheFailsClosed(t *testing.T) {
	store := wsticket.NewStore(nil)
	if _, _, err := store.Issue(context.Background(), 1, ""); err == nil {
		t.Fatal("缓存未初始化时 Issue 应失败")
	}
	if _, err := store.Redeem(context.Background(), "x", ""); !errors.Is(err, wsticket.ErrInvalid) {
		t.Fatalf("缓存未初始化时 Redeem 应返回 ErrInvalid，实际 %v", err)
	}
}

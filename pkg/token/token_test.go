package token_test

import (
	"testing"
	"time"

	"github.com/CycleZero/rym/pkg/token"

	"github.com/golang-jwt/jwt/v5"
)

// TestIssueProducesTokenTransportCanVerify 锁定签发侧与中间件侧的约定：
// HS256 + claim user_id，用同一个密钥可验。
func TestIssueProducesTokenTransportCanVerify(t *testing.T) {
	const secret = "test-secret"
	issuer, err := token.NewIssuer(secret, time.Hour)
	if err != nil {
		t.Fatalf("创建签发器失败：%v", err)
	}

	raw, expiresAt, err := issuer.Issue(42, "alice")
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("过期时间应在未来：%v", expiresAt)
	}

	parsed, err := jwt.Parse(raw, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("令牌校验失败：%v", err)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims 类型不对：%T", parsed.Claims)
	}
	if uid, ok := claims["user_id"].(float64); !ok || uid != 42 {
		t.Fatalf("user_id=%v，期望 42", claims["user_id"])
	}
	if name, _ := claims["username"].(string); name != "alice" {
		t.Fatalf("username=%v，期望 alice", claims["username"])
	}
}

// TestNewIssuerFailsClosed 密钥或 TTL 非法时启动失败，不静默降级。
func TestNewIssuerFailsClosed(t *testing.T) {
	if _, err := token.NewIssuer("", time.Hour); err == nil {
		t.Fatal("空密钥应返回错误")
	}
	if _, err := token.NewIssuer("   ", time.Hour); err == nil {
		t.Fatal("空白密钥应返回错误")
	}
	if _, err := token.NewIssuer("secret", 0); err == nil {
		t.Fatal("TTL=0 应返回错误")
	}
}

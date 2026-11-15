package transport

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/CycleZero/rym/conf"
)

// TestClientIPOnlyTrustsConfiguredProxies 锁定"代理头只在可信对端时才采信"。
//
// 这是安全语义：X-Real-IP / X-Forwarded-For 由客户端控制，无条件采信会让
// 访问日志、审计与限流维度全部失真。
func TestClientIPOnlyTrustsConfiguredProxies(t *testing.T) {
	cases := []struct {
		name       string
		trusted    []string
		remoteAddr string
		realIP     string
		xff        string
		want       string
	}{
		{
			name:       "未配置可信代理时忽略 X-Real-IP",
			remoteAddr: "203.0.113.9:52100",
			realIP:     "1.2.3.4",
			want:       "203.0.113.9",
		},
		{
			name:       "未配置可信代理时忽略 X-Forwarded-For",
			remoteAddr: "203.0.113.9:52100",
			xff:        "1.2.3.4, 10.0.0.1",
			want:       "203.0.113.9",
		},
		{
			name:       "对端不在白名单时忽略代理头",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "203.0.113.9:52100",
			realIP:     "1.2.3.4",
			xff:        "1.2.3.4",
			want:       "203.0.113.9",
		},
		{
			name:       "可信对端优先采信 X-Real-IP",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:443",
			realIP:     "1.2.3.4",
			xff:        "5.6.7.8",
			want:       "1.2.3.4",
		},
		{
			name:       "可信对端无 X-Real-IP 时用 X-Forwarded-For",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:443",
			xff:        "1.2.3.4",
			want:       "1.2.3.4",
		},
		{
			name: "多级代理：从右往左跳过可信，取第一个不可信地址",
			// 最左侧 9.9.9.9 是客户端伪造的，真实客户端是 1.2.3.4
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:443",
			xff:        "9.9.9.9, 1.2.3.4, 10.0.0.2",
			want:       "1.2.3.4",
		},
		{
			name:       "IPv6 单地址白名单（无掩码写法）",
			trusted:    []string{"::1"},
			remoteAddr: "[::1]:443",
			realIP:     "2001:db8::1",
			want:       "2001:db8::1",
		},
		{
			name:       "非法 CIDR 被忽略后不信任任何来源",
			trusted:    []string{"not-a-cidr"},
			remoteAddr: "10.0.0.1:443",
			realIP:     "1.2.3.4",
			want:       "10.0.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newClientIPResolver(tc.trusted, slog.New(slog.DiscardHandler))

			req, err := http.NewRequest(http.MethodGet, "http://example.com/v1/demos", nil)
			if err != nil {
				t.Fatalf("构造请求失败：%v", err)
			}
			req.RemoteAddr = tc.remoteAddr
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			if got := resolver.clientIP(req); got != tc.want {
				t.Errorf("clientIP() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestNewMiddlewaresToleratesInvalidTrustedProxy 配置写错不应让进程起不来，
// 而是降级为"该来源不被信任"（fail-closed）。
func TestNewMiddlewaresToleratesInvalidTrustedProxy(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("非法 trusted_proxies 不应 panic：%v", r)
		}
	}()

	bc := &conf.Bootstrap{
		Server: &conf.Server{Http: &conf.HTTP{
			Host:           "127.0.0.1",
			Port:           8000,
			TrustedProxies: []string{"10.0.0.0/8", "bad-cidr"},
		}},
	}

	if mws := NewMiddlewares(bc, slog.New(slog.DiscardHandler)); mws == nil {
		t.Fatal("NewMiddlewares 返回 nil")
	}
}

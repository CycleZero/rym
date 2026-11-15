package transport

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"gin-template/conf"
	"gin-template/pkg/common"
	"gin-template/pkg/errs"
	"gin-template/pkg/log"
	"gin-template/pkg/response"
	"gin-template/pkg/trace"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/transport"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// 网关身份头：终态由网关做鉴权后下发；本服务在 trust_gateway_identity 时直接信任。
const (
	headerGatewayUserID = "x-md-global-auth-user-id"
	headerAuthorization = "Authorization"
)

// defaultCORSOrigins 本地开发 + Tauri 桌面端的默认白名单。
//
// 注意：绝不使用 "*" + Allow-Credentials 的非法组合（浏览器会拒绝），
// 也绝不把 Origin 原样反射给未授权来源。
var defaultCORSOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
	"http://127.0.0.1:5173",
	"tauri://localhost",
	"http://tauri.localhost",
}

// Middlewares 汇总中间件与过滤器。
//
// 顺序在这里**写死一次**：中间件数组第一个为最外层，过滤器同理。
// 不再依赖包级可变注册表与 "中间件注册未完成" 的 panic。
type Middlewares struct {
	chain   []middleware.Middleware
	filters []khttp.FilterFunc
}

// NewMiddlewares 按固定顺序装配中间件链。
func NewMiddlewares(cfg *conf.Bootstrap, logger *slog.Logger) *Middlewares {
	// 客户端 IP 解析策略只取决于服务端配置，构造期解析一次，调用期零分配。
	resolver := newClientIPResolver(cfg.GetServer().GetHttp().GetTrustedProxies(), logger)

	return &Middlewares{
		chain: []middleware.Middleware{
			recovery.Recovery(recovery.WithLogger(logger)), // panic → 500 统一信封
			traceIDMiddleware(),                            // X-Request-ID 回写
			metadataMiddleware(resolver),                   // 请求元数据 → ctx
			authMiddleware(cfg),                            // 网关身份头 / 本地 JWT
		},
		filters: []khttp.FilterFunc{
			accessLogFilter(logger, resolver), // 最外层：状态码、耗时、响应大小
			traceFilter(cfg.ServiceName()),    // OTel server span（同时回写请求 ID 头）
			corsFilter(cfg.GetServer().GetHttp().GetCorsAllowOrigins()),
		},
	}
}

// traceIDMiddleware 把链路 TraceID 写入响应头 X-Request-ID。
//
// 请求 ID 就是 TraceID：响应头 ≡ 响应体 trace_id ≡ 日志 trace.id ≡ 链路 TraceID，
// 客户端只需一个 ID 就能在日志/链路平台检索整条请求。
func traceIDMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if id := trace.TraceID(ctx); id != "" {
				if tr, ok := transport.FromServerContext(ctx); ok {
					tr.ReplyHeader().Set(response.HeaderRequestID, id)
				}
			}
			return next(ctx, req)
		}
	}
}

// metadataMiddleware 为每个请求补充客户端 IP、UA、TraceID。
func metadataMiddleware(resolver *clientIPResolver) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			md := &common.RequestMetadata{TraceID: trace.TraceID(ctx)}
			if r, ok := khttp.RequestFromServerContext(ctx); ok {
				md.UserAgent = r.UserAgent()
				md.ClientIP = resolver.clientIP(r)
			}
			return next(common.WithMetadata(ctx, md), req)
		}
	}
}

// authMiddleware 身份解析，双模：
//
//  1. trust_gateway_identity=true：直接读网关下发的 x-md-global-auth-user-id（生产终态）
//  2. 否则：本地解析 Authorization: Bearer <JWT>（HS256，显式限定算法）
//
// require_auth=true 时鉴权失败返回 401（统一信封）；默认可选鉴权，
// 只填充身份、不拦截请求，便于灰度迁移到网关。
func authMiddleware(cfg *conf.Bootstrap) middleware.Middleware {
	sec := cfg.GetSecurity()
	optional := !sec.GetRequireAuth()

	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			uid, err := resolveUserID(ctx, sec)
			if err != nil {
				if optional {
					return next(ctx, req)
				}
				return nil, err
			}
			if md := common.Metadata(ctx); md != nil {
				md.UserID = uid
			}
			return next(ctx, req)
		}
	}
}

// resolveUserID 解析当前用户 ID；0 表示匿名。
func resolveUserID(ctx context.Context, sec *conf.Security) (uint, error) {
	tr, ok := transport.FromServerContext(ctx)
	if !ok {
		return 0, nil
	}

	if sec.GetTrustGatewayIdentity() {
		return parseUintHeader(tr.RequestHeader().Get(headerGatewayUserID)), nil
	}

	raw := strings.TrimPrefix(tr.RequestHeader().Get(headerAuthorization), "Bearer ")
	if raw == "" {
		return 0, nil
	}

	token, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		return []byte(sec.GetJwtSecret()), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return 0, errs.Unauthorized("无效的认证令牌").WithCause(err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, errs.Unauthorized("无效的认证令牌")
	}
	if v, ok := claims["user_id"]; ok {
		switch n := v.(type) {
		case float64:
			return uint(n), nil
		case string:
			return parseUintHeader(n), nil
		}
	}
	return 0, nil
}

// accessLogFilter 结构化访问日志；状态码/响应大小需要直接包 ResponseWriter。
func accessLogFilter(logger *slog.Logger, resolver *clientIPResolver) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(sw, r)

			log.WithContext(logger, r.Context()).Info("http 请求",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.statusCode(),
				"cost_ms", time.Since(start).Milliseconds(),
				"client_ip", resolver.clientIP(r),
				"resp_size", sw.bytes,
				"request_id", sw.Header().Get(response.HeaderRequestID),
			)
		})
	}
}

// traceFilter 用 otelhttp 产生 server span，并保证响应写出前带上请求 ID。
func traceFilter(serviceName string) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := trace.TraceID(r.Context()); id != "" {
				w.Header().Set(response.HeaderRequestID, id)
			}
			next.ServeHTTP(w, r)
		}), serviceName)
	}
}

// corsFilter 白名单 CORS。
//
// 规则：
//   - 只回显命中白名单的 Origin，并输出 Vary: Origin（缓存安全）
//   - 不使用 "*" + Allow-Credentials（非法组合）
//   - 预检 OPTIONS 直接 204，不进入业务路由
func corsFilter(configured []string) khttp.FilterFunc {
	allow := configured
	if len(allow) == 0 {
		allow = defaultCORSOrigins
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin, allow) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
				h.Set("Access-Control-Max-Age", "86400")
				h.Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(origin string, allow []string) bool {
	return slices.Contains(allow, origin)
}

// statusWriter 记录状态码与响应体大小。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap 支持 http.ResponseController 访问底层 writer（Flush/超时等）。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// clientIPResolver 解析客户端 IP，代理头只在"对端可信"时才采信。
//
// 规则（fail-closed）：
//   - TCP 对端不在 trusted_proxies 里 → 一律用对端地址，忽略一切代理头；
//   - 在 trusted_proxies 里 → 采信 X-Real-IP，其次 X-Forwarded-For；
//   - X-Forwarded-For 从右往左跳过可信代理，第一个不可信地址即真实客户端
//     （多级代理下这样才不会被最左侧伪造值骗到）。
//
// 为什么不无条件读代理头：这两个头客户端可随意伪造，无条件采信会让
// 访问日志、审计与限流维度全部失真。
type clientIPResolver struct {
	trusted []*net.IPNet
}

// newClientIPResolver 解析 CIDR 白名单。非法条目跳过并告警：
// 结果是"该来源不被信任"，即回退到 TCP 对端地址——安全的失败方向。
func newClientIPResolver(cidrs []string, logger *slog.Logger) *clientIPResolver {
	r := &clientIPResolver{}
	for _, raw := range cidrs {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		// 允许直接写单个 IP（补成 /32 或 /128）
		if !strings.Contains(cidr, "/") {
			if ip := net.ParseIP(cidr); ip != nil {
				if ip.To4() != nil {
					cidr += "/32"
				} else {
					cidr += "/128"
				}
			}
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			if logger != nil {
				logger.Warn("trusted_proxies 含非法 CIDR，已忽略（该来源不会被信任）", "value", raw)
			}
			continue
		}
		r.trusted = append(r.trusted, ipNet)
	}
	return r
}

// contains 判断 IP 是否在可信代理白名单内。
func (r *clientIPResolver) contains(ip net.IP) bool {
	if r == nil || ip == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP 返回用于日志/审计的客户端 IP 字符串。
func (r *clientIPResolver) clientIP(req *http.Request) string {
	if req == nil {
		return ""
	}

	peer := remoteIP(req)
	if peer == nil {
		return req.RemoteAddr
	}
	// 对端不可信：代理头一律丢弃（否则任何人都能伪造来源 IP）
	if !r.contains(peer) {
		return peer.String()
	}

	if ip := parseIPCandidate(req.Header.Get("X-Real-IP")); ip != nil {
		return ip.String()
	}

	// X-Forwarded-For 形如 "client, proxy1, proxy2"，从右往左找第一个不可信地址
	parts := strings.Split(req.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := parseIPCandidate(parts[i])
		if ip == nil || r.contains(ip) {
			continue
		}
		return ip.String()
	}

	return peer.String()
}

// remoteIP 取 TCP 对端地址。
func remoteIP(req *http.Request) net.IP {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// parseIPCandidate 解析单个候选地址（兼容 "1.2.3.4" 与 "[::1]:8080"）。
func parseIPCandidate(v string) net.IP {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if ip := net.ParseIP(v); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(v); err == nil {
		return net.ParseIP(strings.TrimSpace(host))
	}
	return nil
}

func parseUintHeader(v string) uint {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}

// Package token 负责签发终端用户访问令牌（JWT / HS256）。
//
// 为什么单独一个包：
//   - 签发只发生在登录成功之后，属于基础设施能力，不放进 biz（biz 不碰密钥）
//   - 校验已经在 pkg/transport 的 authMiddleware 里（读 Authorization: Bearer）
//   - 两边必须共用同一套约定：HS256、claim user_id、同一个 security.jwt_secret
//
// 本包只做**签发**，不做校验：校验由中间件统一负责，避免出现两份实现。
package token

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer 用固定密钥签发用户令牌。
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// NewIssuer 创建签发器。
//
// 密钥为空时返回错误而不是静默降级：没有密钥就无法签发能被中间件验过的令牌，
// 启动阶段失败比运行期所有登录都 500 更好排查。
func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("token: jwt_secret 不能为空（user 服务需要签发登录令牌）")
	}
	if ttl <= 0 {
		return nil, errors.New("token: token_ttl_seconds 必须大于 0")
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

// Issue 为指定用户签发令牌，返回令牌串与过期时间。
//
// claim 约定（与 pkg/transport/authMiddleware 对齐）：
//   - user_id：uint64，中间件据此填充请求身份
//   - username：便于日志/排查，不参与鉴权
//   - iat / exp：签发与过期时间（jwt.Parse 默认校验 exp）
func (i *Issuer) Issue(userID uint64, username string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(i.ttl)
	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		"iat":      now.Unix(),
		"exp":      expiresAt.Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

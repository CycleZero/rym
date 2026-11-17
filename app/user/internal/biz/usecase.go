package biz

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CycleZero/rym/pkg/errs"

	"golang.org/x/crypto/bcrypt"
)

// usernamePattern 登录名规则：3-32 位小写字母、数字、下划线。
//
// 统一小写是关键：MySQL 默认排序规则大小写不敏感，SQLite 唯一索引大小写敏感。
// 如果原样存储，"Alice" 在 MySQL 里会占用 "alice"，在 SQLite 里却不会——
// 同一套代码两种行为。存储前统一小写，两边语义就一致了。
var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// dummyPasswordHash 用于"用户名不存在"时也做一次 bcrypt 比较，
// 让失败路径的耗时与"密码错误"对齐，降低通过响应时间枚举账号的可能。
// 明文是 "rym-dummy-password"，cost=10（与生产默认一致）。
const dummyPasswordHash = "$2a$10$XUsrImNFvk.10Z28Vk.w1uYST8uKHgFzKSVzHXqKEn9XIlcx.Ctdq"

// Usecase 用户业务编排。
type Usecase struct {
	repo     Repo
	settings Settings
}

// NewUsecase 由 Wire 注入仓储与业务参数。
func NewUsecase(repo Repo, settings Settings) *Usecase {
	return &Usecase{repo: repo, settings: settings}
}

// RegisterInput 注册入参。
type RegisterInput struct {
	Username string
	Password string
	Nickname string
}

// Register 注册新用户。
//
// 幂等/并发：用户名唯一索引是最终闸门；data 层把唯一键冲突翻译成
// ErrUsernameTaken，因此这里不需要先查一次（少一次往返，也少一个竞态窗口）。
func (u *Usecase) Register(ctx context.Context, in RegisterInput) (*User, error) {
	username := normalizeUsername(in.Username)
	if !usernamePattern.MatchString(username) {
		return nil, errs.InvalidArgument("用户名需为 3-32 位小写字母、数字或下划线")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	nickname, err := normalizeNickname(in.Nickname, username)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), u.settings.BcryptCost)
	if err != nil {
		return nil, errs.Internal("生成密码哈希失败").WithCause(err)
	}

	now := time.Now().Truncate(time.Millisecond)
	user := &User{
		Username:     username,
		PasswordHash: string(hash),
		Nickname:     nickname,
		Status:       UserStatusNormal,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := u.repo.Create(ctx, user); err != nil {
		return nil, wrapInternal("注册失败", err)
	}
	return user, nil
}

// LoginInput 登录入参。
type LoginInput struct {
	Username string
	Password string
}

// Login 校验用户名与密码。
//
// 安全细节：
//   - 用户不存在与密码错误返回同一个错误（ErrInvalidCredentials）
//   - 用户不存在时也执行一次 bcrypt 比较，避免响应时间泄漏账号是否存在
//   - 账号禁用只在密码正确后才提示（否则同样是在泄漏账号存在性）
//
// 令牌签发不在这里：biz 不碰密钥，由 service 层调用 pkg/token 完成。
func (u *Usecase) Login(ctx context.Context, in LoginInput) (*User, error) {
	username := normalizeUsername(in.Username)

	user, err := u.repo.GetByUsername(ctx, username)
	if err != nil {
		return nil, wrapInternal("查询用户失败", err)
	}
	if user == nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(in.Password))
		return nil, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	if user.Status != UserStatusNormal {
		return nil, ErrAccountDisabled
	}
	return user, nil
}

// GetByID 按 id 取用户（GetMe 用）。
func (u *Usecase) GetByID(ctx context.Context, id uint64) (*User, error) {
	if id == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	user, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return nil, wrapInternal("查询用户失败", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	if user.Status != UserStatusNormal {
		return nil, ErrAccountDisabled
	}
	return user, nil
}

// normalizeUsername 去空格并转小写。
func normalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validatePassword 长度策略：只限长度，不强制"大小写+符号"混合。
//
// 依据 NIST SP 800-63B：长度比复杂度更有效，复杂规则反而导致弱密码规律化。
// 72 字节是 bcrypt 的硬上限（超出会被静默截断），必须显式拒绝。
func validatePassword(password string) error {
	if len(password) < 8 {
		return errs.InvalidArgument("密码至少 8 位")
	}
	if len(password) > 72 {
		return errs.InvalidArgument("密码不能超过 72 字节")
	}
	return nil
}

// normalizeNickname 去空格、限长；为空时回退为用户名。
func normalizeNickname(raw, username string) (string, error) {
	nickname := strings.TrimSpace(raw)
	if nickname == "" {
		return username, nil
	}
	if utf8.RuneCountInString(nickname) > 32 {
		return "", errs.InvalidArgument("昵称不能超过 32 个字符")
	}
	return nickname, nil
}

// wrapInternal 把基础设施错误归一化为统一内部错误。
// 已经是 *errs.Error 的（含 ErrUsernameTaken）原样返回，保留业务码与状态码。
func wrapInternal(message string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.Internal(message).WithCause(err)
}

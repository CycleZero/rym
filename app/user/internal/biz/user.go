// Package biz 是用户模块的业务层（领域模型 + 仓储接口 + 用例）。
//
// 约束与 app/demo、app/im 一致：
//   - 零 proto 依赖：对外契约转换在 service 层
//   - 不出现 *gorm.DB / *sql.Tx：仓储接口定义在这里（依赖倒置）
//   - 不记错误日志：统一由 pkg/response.EncodeError 收口
//
// v1 只解决"能注册、能登录、能认出当前用户"，不做手机号验证、找回密码、
// 第三方登录、角色权限（留给组织/权限模块）。
package biz

import (
	"context"
	"time"

	"github.com/CycleZero/rym/pkg/errs"
)

// 用户状态。数值与数据库保持一致。
const (
	UserStatusDisabled int32 = 0
	UserStatusNormal   int32 = 1
)

// 对外错误。
var (
	// ErrUsernameTaken 用户名冲突（唯一索引兜底，预检查只在常见路径）。
	ErrUsernameTaken = errs.Conflict("用户名已被占用")

	// ErrInvalidCredentials 登录失败。用户名不存在与密码错误统一用这一条，
	// 避免客户端通过错误信息枚举账号。
	ErrInvalidCredentials = errs.Unauthorized("用户名或密码错误")

	// ErrAccountDisabled 账号被禁用；只有密码校验通过后才会返回，
	// 否则等于告诉攻击者"这个账号存在但被禁用了"。
	ErrAccountDisabled = errs.Forbidden("账号已被禁用")

	// ErrUserNotFound 仓储层的"查无此人"，由 service 转成 401（令牌已失效），
	// 不直接下发 404。
	ErrUserNotFound = errs.NotFound("用户不存在")
)

// User 领域模型。
//
// PasswordHash 只在 biz/data 之间流转，service 层转换时绝不带出去。
type User struct {
	ID           uint64
	Username     string
	PasswordHash string
	Nickname     string
	Status       int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Repo 定义用户的持久化能力。
type Repo interface {
	// Create 写入新用户；用户名冲突时返回 ErrUsernameTaken。
	Create(ctx context.Context, user *User) error

	// GetByUsername 按登录名查询；不存在返回 nil, nil。
	GetByUsername(ctx context.Context, username string) (*User, error)

	// GetByID 按主键查询；不存在返回 nil, nil。
	GetByID(ctx context.Context, id uint64) (*User, error)
}

// Settings 用户模块的业务参数；由组合根从服务配置转换（biz 不 import conf）。
type Settings struct {
	// BcryptCost bcrypt 计算强度；测试可以调低以缩短耗时。
	BcryptCost int
}

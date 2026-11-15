// Package biz 是 demo 模块的业务层（领域模型 + 仓储接口 + 用例）。
//
// 约束：
//   - 零 proto 依赖（不 import api/），对外契约转换在 service 层
//   - 不出现 *gorm.DB / *sql.Tx：事务边界通过 tx.TxManager 表达
//   - 仓储接口定义在这里（依赖倒置），实现由 data 层提供
package biz

import (
	"context"
	"time"

	"github.com/CycleZero/rym/pkg/errs"
)

// ErrDemoNotFound 领域错误：记录不存在。
//
// data 层把 gorm.ErrRecordNotFound 翻译成它，上层不必依赖 ORM 的错误类型；
// pkg/errs.Resolve 会把它渲染成 404 / 40400。
var ErrDemoNotFound = errs.NotFound("记录不存在")

// Demo 领域模型。
//
// 不带任何 ORM / JSON / proto 标签：持久化细节在 data，传输细节在 service。
type Demo struct {
	ID          uint64
	Name        string
	Description string
	Status      int32
	CreatedBy   uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Repo 定义 demo 的持久化能力。
type Repo interface {
	Create(ctx context.Context, demo *Demo) error
	GetByID(ctx context.Context, id uint64) (*Demo, error)
	List(ctx context.Context, page, pageSize int) ([]*Demo, int64, error)
	Update(ctx context.Context, demo *Demo) error
	Delete(ctx context.Context, id uint64) error
}

// CreateInput 创建用例入参（biz 自有 DTO，不依赖 proto）。
type CreateInput struct {
	Name        string
	Description string
	CreatedBy   uint64
}

// UpdateInput 更新用例入参。
type UpdateInput struct {
	Name        string
	Description string
}

// Settings 分页等业务参数；由组合根从服务配置转换（biz 不 import conf）。
type Settings struct {
	DefaultPageSize int
	MaxPageSize     int
}

// wrapInternal 把基础设施错误归一化为统一内部错误。
//
// 已经是 *errs.Error 的错误（含领域错误）原样返回，保留业务码与状态码；
// 其余（SQL/网络错误）包装为 500，细节只进日志、不进入响应体。
func wrapInternal(message string, err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.Internal(message).WithCause(err)
}

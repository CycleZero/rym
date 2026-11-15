package biz

import (
	"context"
	"strings"

	"gin-template/pkg/errs"
	"gin-template/pkg/tx"
)

// Usecase demo 业务编排。
//
// 事务边界在这里控制：Create/Update 通过 tx.WithTx 保证"读-改-写"原子，
// 仓储层（data）用 tx.WithContext 透明复用同一个事务。
//
// 本层**不记错误日志**：错误统一由传输层的 ErrorEncoder 收口
// （`pkg/response.EncodeError` 只对 5xx 记 Error，4xx 不写日志）。
// 若在这里也记一遍，一次失败会产生两条日志，且 404 这类请求方错误会被
// 记成 Error 级别、制造告警噪声。需要补充业务上下文时，用
// `errs.Xxx(...).WithCause(err)` 把信息带进 cause，仍由单一出口输出。
type Usecase struct {
	repo     Repo
	tm       tx.TxManager
	settings Settings
}

// NewUsecase 由 Wire 注入仓储接口、事务管理器与分页设置。
func NewUsecase(repo Repo, tm tx.TxManager, settings Settings) *Usecase {
	return &Usecase{repo: repo, tm: tm, settings: settings}
}

// Create 创建记录。
func (u *Usecase) Create(ctx context.Context, in CreateInput) (*Demo, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errs.InvalidArgument("名称不能为空")
	}

	demo := &Demo{
		Name:        name,
		Description: in.Description,
		Status:      1, // 新建默认启用
		CreatedBy:   in.CreatedBy,
	}

	if err := u.tm.WithTx(ctx, func(txCtx context.Context) error {
		return u.repo.Create(txCtx, demo)
	}); err != nil {
		return nil, wrapInternal("创建 Demo 失败", err)
	}
	return demo, nil
}

// GetByID 获取单条记录。
func (u *Usecase) GetByID(ctx context.Context, id uint64) (*Demo, error) {
	demo, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return nil, wrapInternal("查询 Demo 失败", err)
	}
	return demo, nil
}

// List 分页查询；页码与页大小在这里归一化（超限截断，不报错）。
func (u *Usecase) List(ctx context.Context, page, pageSize int) ([]*Demo, int64, error) {
	page, pageSize = u.NormalizePage(page, pageSize)

	demos, total, err := u.repo.List(ctx, page, pageSize)
	if err != nil {
		return nil, 0, wrapInternal("查询 Demo 列表失败", err)
	}
	return demos, total, nil
}

// Update 更新记录：读-改-写放在同一事务里，避免并发覆盖产生半更新。
func (u *Usecase) Update(ctx context.Context, id uint64, in UpdateInput) (*Demo, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errs.InvalidArgument("名称不能为空")
	}

	var updated *Demo
	err := u.tm.WithTx(ctx, func(txCtx context.Context) error {
		demo, err := u.repo.GetByID(txCtx, id)
		if err != nil {
			return err
		}
		demo.Name = name
		demo.Description = in.Description
		if err := u.repo.Update(txCtx, demo); err != nil {
			return err
		}
		updated = demo
		return nil
	})
	if err != nil {
		return nil, wrapInternal("更新 Demo 失败", err)
	}
	return updated, nil
}

// Delete 删除记录。
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if err := u.repo.Delete(ctx, id); err != nil {
		return wrapInternal("删除 Demo 失败", err)
	}
	return nil
}

// NormalizePage 归一化分页参数：缺省用默认值，超上限截断。
//
// service 层需要回填实际生效的 page/page_size，因此该方法导出。
func (u *Usecase) NormalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = u.settings.DefaultPageSize
	}
	if max := u.settings.MaxPageSize; max > 0 && pageSize > max {
		pageSize = max
	}
	return page, pageSize
}

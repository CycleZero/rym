package data

import (
	"context"
	"errors"

	"github.com/CycleZero/rym/app/demo/internal/biz"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/gorm"
)

// demoRepo 实现 biz.Repo。
//
// 所有数据库操作统一走 tx.WithContext(ctx, r.db)：ctx 里带事务时自动复用，
// 否则退回普通连接——biz 控制事务边界，data 层无需感知。
type demoRepo struct {
	db *gorm.DB
}

// NewDemoRepo 由 Wire 注入 *gorm.DB。
func NewDemoRepo(db *gorm.DB) *demoRepo {
	return &demoRepo{db: db}
}

// Create 写入一条记录，并把数据库生成的字段回填到领域模型。
func (r *demoRepo) Create(ctx context.Context, demo *biz.Demo) error {
	po := fromBiz(demo)
	if err := tx.WithContext(ctx, r.db).Create(po).Error; err != nil {
		return err
	}
	demo.ID = po.ID
	demo.CreatedAt = po.CreatedAt
	demo.UpdatedAt = po.UpdatedAt
	return nil
}

// GetByID 按主键查询；未命中返回领域错误（上层不依赖 gorm.ErrRecordNotFound）。
func (r *demoRepo) GetByID(ctx context.Context, id uint64) (*biz.Demo, error) {
	var po DemoPO
	if err := tx.WithContext(ctx, r.db).First(&po, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrDemoNotFound
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// List 分页查询。
//
// count 与 find 各用独立 query：虽然新版 GORM 会在 Count 后恢复 SELECT/ORDER BY，
// 但显式分开更稳妥，也不依赖 ORM 的内部状态恢复行为。
func (r *demoRepo) List(ctx context.Context, page, pageSize int) ([]*biz.Demo, int64, error) {
	db := tx.WithContext(ctx, r.db)

	var total int64
	if err := db.Model(&DemoPO{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var pos []DemoPO
	offset := (page - 1) * pageSize
	if err := db.Model(&DemoPO{}).
		Offset(offset).Limit(pageSize).Order("id DESC").
		Find(&pos).Error; err != nil {
		return nil, 0, err
	}

	demos := make([]*biz.Demo, 0, len(pos))
	for i := range pos {
		demos = append(demos, pos[i].toBiz())
	}
	return demos, total, nil
}

// Update 全字段写回；先确认记录存在（未命中返回领域错误），并保留软删除标记。
func (r *demoRepo) Update(ctx context.Context, demo *biz.Demo) error {
	db := tx.WithContext(ctx, r.db)

	var existing DemoPO
	if err := db.First(&existing, demo.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return biz.ErrDemoNotFound
		}
		return err
	}

	po := fromBiz(demo)
	po.DeletedAt = existing.DeletedAt
	po.CreatedAt = existing.CreatedAt

	if err := db.Save(po).Error; err != nil {
		return err
	}
	demo.UpdatedAt = po.UpdatedAt
	return nil
}

// Delete 按主键软删除。
func (r *demoRepo) Delete(ctx context.Context, id uint64) error {
	result := tx.WithContext(ctx, r.db).Delete(&DemoPO{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return biz.ErrDemoNotFound
	}
	return nil
}

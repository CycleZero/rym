package data

import (
	"github.com/CycleZero/rym/app/demo/internal/biz"
)

// toBiz 把数据库模型转换为领域模型（逐字段 1:1）。
func (po *DemoPO) toBiz() *biz.Demo {
	return &biz.Demo{
		ID:          po.ID,
		Name:        po.Name,
		Description: po.Description,
		Status:      po.Status,
		CreatedBy:   po.CreatedBy,
		CreatedAt:   po.CreatedAt,
		UpdatedAt:   po.UpdatedAt,
	}
}

// fromBiz 把领域模型完整转换为数据库模型。
//
// 忠实映射、不做字段裁剪：领域模型新增字段时只需改这一处，
// 不会出现"某个字段被静默丢弃"。deleted_at 属仓储侧状态，不在领域模型里。
func fromBiz(d *biz.Demo) *DemoPO {
	return &DemoPO{
		ID:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Status:      d.Status,
		CreatedBy:   d.CreatedBy,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
	}
}

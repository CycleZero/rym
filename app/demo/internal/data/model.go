package data

import (
	"time"

	"gorm.io/gorm"
)

// DemoPO 是 demo 表的数据库模型（PO）。
//
// 只在 data 层内部使用：对外数据形态是 biz.Demo，转换集中在 convert.go。
// 表结构变更走 app/demo/migrations/*.sql，生产代码不做自动迁移。
type DemoPO struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement;comment:主键"`
	Name        string         `gorm:"type:varchar(100);not null;comment:名称"`
	Description string         `gorm:"type:text;comment:描述"`
	Status      int32          `gorm:"type:tinyint;default:1;comment:状态 1=正常 0=禁用"`
	CreatedBy   uint64         `gorm:"comment:创建人ID"`
	CreatedAt   time.Time      `gorm:"comment:创建时间"`
	UpdatedAt   time.Time      `gorm:"comment:更新时间"`
	DeletedAt   gorm.DeletedAt `gorm:"index;comment:软删除时间"`
}

// TableName 固定表名，避免依赖 GORM 的复数化规则。
func (DemoPO) TableName() string { return "demo" }

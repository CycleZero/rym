package data

import "time"

// UserPO 是 users 表的数据库模型（PO）。
//
// 表结构以 app/user/migrations/001_init_mysql.sql 为准，
// SQLite 版逻辑等价。生产代码不做自动迁移。
type UserPO struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	Username     string
	PasswordHash string
	Nickname     string
	Status       int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName 固定表名，避免依赖 GORM 的复数化规则。
func (UserPO) TableName() string { return "users" }

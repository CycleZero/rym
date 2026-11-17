package data

import (
	"context"
	"errors"

	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/gorm"
)

// repo 实现 biz.Repo。
//
// 所有数据库操作统一走 tx.WithContext(ctx, r.db)：ctx 里带事务时自动复用，
// 否则退回普通连接。注册只有一条 INSERT，v1 不需要事务边界。
type repo struct {
	db *gorm.DB
}

// NewRepo 由 Wire 注入 *gorm.DB。
func NewRepo(db *gorm.DB) *repo {
	return &repo{db: db}
}

// Create 写入新用户。
//
// 用户名唯一索引是并发下的最终闸门。这里不解析驱动错误码（MySQL 1062 /
// SQLite SQLITE_CONSTRAINT 不一致），而是插入失败后回查一次：
// 查得到同名用户就翻译成 ErrUsernameTaken，否则把原始错误交给上层。
func (r *repo) Create(ctx context.Context, user *biz.User) error {
	po := fromBiz(user)
	if err := tx.WithContext(ctx, r.db).Create(po).Error; err != nil {
		var existing UserPO
		findErr := tx.WithContext(ctx, r.db).Where("username = ?", user.Username).First(&existing).Error
		if findErr == nil {
			return biz.ErrUsernameTaken
		}
		return err
	}
	user.ID = po.ID
	user.CreatedAt = po.CreatedAt
	user.UpdatedAt = po.UpdatedAt
	return nil
}

// GetByUsername 按登录名查询；不存在返回 nil, nil。
func (r *repo) GetByUsername(ctx context.Context, username string) (*biz.User, error) {
	var po UserPO
	err := tx.WithContext(ctx, r.db).Where("username = ?", username).First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// GetByID 按主键查询；不存在返回 nil, nil。
func (r *repo) GetByID(ctx context.Context, id uint64) (*biz.User, error) {
	var po UserPO
	err := tx.WithContext(ctx, r.db).First(&po, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return po.toBiz(), nil
}

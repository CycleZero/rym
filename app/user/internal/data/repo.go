package data

import (
	"context"
	"errors"
	"strings"

	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// GetByIDs 批量查询正常状态用户；不存在的 id 直接忽略。
func (r *repo) GetByIDs(ctx context.Context, ids []uint64) ([]*biz.User, error) {
	if len(ids) == 0 {
		return []*biz.User{}, nil
	}
	var pos []UserPO
	if err := tx.WithContext(ctx, r.db).
		Where("id IN ? AND status = ?", ids, biz.UserStatusNormal).
		Order("id ASC").
		Find(&pos).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.User, 0, len(pos))
	for i := range pos {
		out = append(out, pos[i].toBiz())
	}
	return out, nil
}

// Search 按 username / nickname 前缀搜索。
//
// 排序：精确 username > username 前缀 > nickname 前缀 > id。
// LIKE 的 % / _ / ! 必须转义，否则用户输入 "%" 会变成全表匹配。
// 只用前缀（kw%），不使用前导 %kw%，否则 B-Tree 索引失效。
func (r *repo) Search(ctx context.Context, keyword string, excludeID uint64, limit int) ([]*biz.User, error) {
	like := escapeLike(keyword) + "%"

	var pos []UserPO
	err := tx.WithContext(ctx, r.db).
		Where("status = ? AND id <> ?", biz.UserStatusNormal, excludeID).
		Where("(username LIKE ? ESCAPE '!' OR nickname_lower LIKE ? ESCAPE '!')", like, like).
		Order(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN username = ? THEN 0 WHEN username LIKE ? ESCAPE '!' THEN 1 ELSE 2 END",
			Vars: []any{keyword, like},
		}}).
		Order("id ASC").
		Limit(limit).
		Find(&pos).Error
	if err != nil {
		return nil, err
	}
	out := make([]*biz.User, 0, len(pos))
	for i := range pos {
		out = append(out, pos[i].toBiz())
	}
	return out, nil
}

// escapeLike 转义 LIKE 的通配符；ESCAPE '!' 与 MySQL/SQLite 都兼容。
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
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

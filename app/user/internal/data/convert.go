package data

import (
	"github.com/CycleZero/rym/app/user/internal/biz"
)

// toBiz 把用户 PO 转换为领域模型（逐字段 1:1）。
func (po *UserPO) toBiz() *biz.User {
	if po == nil {
		return nil
	}
	return &biz.User{
		ID:           po.ID,
		Username:     po.Username,
		PasswordHash: po.PasswordHash,
		Nickname:     po.Nickname,
		Status:       po.Status,
		CreatedAt:    po.CreatedAt,
		UpdatedAt:    po.UpdatedAt,
	}
}

// fromBiz 把领域模型完整转换为 PO。
func fromBiz(u *biz.User) *UserPO {
	return &UserPO{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		Nickname:     u.Nickname,
		Status:       u.Status,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

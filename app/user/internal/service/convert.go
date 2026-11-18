package service

import (
	"time"

	v1 "github.com/CycleZero/rym/api/user/v1"
	"github.com/CycleZero/rym/app/user/internal/biz"
)

// TimeLayout 对外时间格式：RFC3339（与其他服务一致）。
const TimeLayout = time.RFC3339

// toProtoUser 把领域用户转换为 proto 契约。
//
// 注意：**只映射公开字段**。PasswordHash 绝不能出现在这里，
// 这是"密码永不下发"的唯一出口，转换测试会断言原始 JSON 不含 password。
func toProtoUser(u *biz.User) *v1.User {
	if u == nil {
		return nil
	}
	return &v1.User{
		Id:        u.ID,
		Username:  u.Username,
		Nickname:  u.Nickname,
		Status:    u.Status,
		CreatedAt: formatTime(u.CreatedAt),
		UpdatedAt: formatTime(u.UpdatedAt),
	}
}

// toProtoUserList 批量转换（保持非 nil 切片，避免 JSON 输出 null）。
func toProtoUserList(users []*biz.User) []*v1.User {
	out := make([]*v1.User, 0, len(users))
	for _, u := range users {
		out = append(out, toProtoUser(u))
	}
	return out
}

// formatTime 统一时间格式；零值输出空串（对外不暴露 0001-01-01）。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(TimeLayout)
}

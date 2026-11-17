package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/CycleZero/rym/api/user/v1"
	"github.com/CycleZero/rym/app/user/internal/biz"
)

// TestToProtoUserMapsPublicFields 锁定 biz → proto 的映射，并确保密码哈希不外泄。
func TestToProtoUserMapsPublicFields(t *testing.T) {
	created := time.Date(2026, 11, 17, 8, 9, 10, 0, time.UTC)

	got := toProtoUser(&biz.User{
		ID:           7,
		Username:     "alice",
		PasswordHash: "$2a$10$should-never-leak",
		Nickname:     "Alice",
		Status:       biz.UserStatusNormal,
		CreatedAt:    created,
		UpdatedAt:    created.Add(time.Minute),
	})

	if got.GetId() != 7 ||
		got.GetUsername() != "alice" ||
		got.GetNickname() != "Alice" ||
		got.GetStatus() != biz.UserStatusNormal ||
		got.GetCreatedAt() != "2026-11-17T08:09:10Z" ||
		got.GetUpdatedAt() != "2026-11-17T08:10:10Z" {
		t.Fatalf("用户字段映射不完整：%+v", got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if strings.Contains(string(raw), "should-never-leak") || strings.Contains(string(raw), "password") {
		t.Fatalf("对外契约泄漏了密码：%s", raw)
	}
}

func TestToProtoUserNil(t *testing.T) {
	if got := toProtoUser(nil); got != nil {
		t.Fatalf("nil 用户应返回 nil，实际 %+v", got)
	}
}

func TestToProtoUserZeroTime(t *testing.T) {
	got := toProtoUser(&biz.User{Username: "alice"})
	if got.GetCreatedAt() != "" || got.GetUpdatedAt() != "" {
		t.Fatalf("零值时间应输出空串，实际 created=%q updated=%q", got.GetCreatedAt(), got.GetUpdatedAt())
	}
}

// 编译期确认 proto 枚举/常量与 biz 状态一致（防止以后改数字不同步）。
var _ = v1.User{Status: biz.UserStatusNormal}

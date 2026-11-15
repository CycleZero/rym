package service

import (
	"time"

	v1 "github.com/CycleZero/rym/api/demo/v1"
	"github.com/CycleZero/rym/app/demo/internal/biz"
)

// TimeLayout 对外时间格式：RFC3339（与 proto 注释一致）。
const TimeLayout = time.RFC3339

// toProto 把领域模型转换为 proto 契约。
//
// 必须逐字段映射：漏映射会被 convert_test.go 的 round-trip 测试抓住。
func toProto(d *biz.Demo) *v1.Demo {
	if d == nil {
		return nil
	}
	return &v1.Demo{
		Id:          d.ID,
		Name:        d.Name,
		Description: d.Description,
		Status:      d.Status,
		CreatedBy:   d.CreatedBy,
		CreatedAt:   formatTime(d.CreatedAt),
		UpdatedAt:   formatTime(d.UpdatedAt),
	}
}

// toProtoList 批量转换（保持非 nil 切片，避免 JSON 输出 null）。
func toProtoList(demos []*biz.Demo) []*v1.Demo {
	out := make([]*v1.Demo, 0, len(demos))
	for _, d := range demos {
		out = append(out, toProto(d))
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

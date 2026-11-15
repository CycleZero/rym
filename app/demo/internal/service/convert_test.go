package service

import (
	"testing"
	"time"

	v1 "gin-template/api/demo/v1"
	"gin-template/app/demo/internal/biz"
)

// TestToProtoMapsEveryField 锁定 biz → proto 的映射完整性。
//
// 漏映射会让客户端拿不到数据，而编译期不会报错，因此这里逐字段断言；
// 新增字段时本测试必须同步更新（PR 里可见）。
func TestToProtoMapsEveryField(t *testing.T) {
	created := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	got := toProto(&biz.Demo{
		ID:          11,
		Name:        "n",
		Description: "d",
		Status:      1,
		CreatedBy:   9,
		CreatedAt:   created,
		UpdatedAt:   created.Add(time.Minute),
	})

	want := &v1.Demo{
		Id:          11,
		Name:        "n",
		Description: "d",
		Status:      1,
		CreatedBy:   9,
		CreatedAt:   "2026-05-06T07:08:09Z",
		UpdatedAt:   "2026-05-06T07:09:09Z",
	}

	if got.GetId() != want.GetId() ||
		got.GetName() != want.GetName() ||
		got.GetDescription() != want.GetDescription() ||
		got.GetStatus() != want.GetStatus() ||
		got.GetCreatedBy() != want.GetCreatedBy() ||
		got.GetCreatedAt() != want.GetCreatedAt() ||
		got.GetUpdatedAt() != want.GetUpdatedAt() {
		t.Errorf("字段映射不完整：\n got = %+v\nwant = %+v", got, want)
	}
}

func TestToProtoNil(t *testing.T) {
	if got := toProto(nil); got != nil {
		t.Errorf("nil 领域模型应返回 nil，实际 %+v", got)
	}
}

// TestToProtoZeroTime 零值时间输出空串，避免把 0001-01-01 暴露给客户端。
func TestToProtoZeroTime(t *testing.T) {
	got := toProto(&biz.Demo{ID: 1})
	if got.GetCreatedAt() != "" || got.GetUpdatedAt() != "" {
		t.Errorf("零值时间应输出空串，实际 created=%q updated=%q", got.GetCreatedAt(), got.GetUpdatedAt())
	}
}

// TestToProtoListKeepsEmptySlice 空列表必须序列化为 []（不能是 null）。
func TestToProtoListKeepsEmptySlice(t *testing.T) {
	got := toProtoList(nil)
	if got == nil {
		t.Fatal("空列表应返回非 nil 切片")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

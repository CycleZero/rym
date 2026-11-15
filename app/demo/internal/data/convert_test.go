package data

import (
	"testing"
	"time"

	"gin-template/app/demo/internal/biz"
)

// TestFromBizToBizRoundTrip 锁定「领域模型 ↔ PO」映射是完整的：
// biz.Demo 的每个字段都必须被 fromBiz 写入 PO，并能被 toBiz 原样读回。
//
// 将来给 biz.Demo 加字段而忘记改映射，这里会因结构体不等而失败，
// 而不是等线上发现某列没写进去。
func TestFromBizToBizRoundTrip(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	src := &biz.Demo{
		ID:          7,
		Name:        "名称",
		Description: "描述",
		Status:      1,
		CreatedBy:   42,
		CreatedAt:   created,
		UpdatedAt:   created.Add(time.Hour),
	}

	po := fromBiz(src)
	if got := po.toBiz(); *got != *src {
		t.Errorf("域模型往返转换后不一致：\n got = %+v\nwant = %+v", *got, *src)
	}
}

// TestFromBizLeavesSoftDeleteAlone 软删除状态属于仓储侧，不应由领域模型写入。
func TestFromBizLeavesSoftDeleteAlone(t *testing.T) {
	po := fromBiz(&biz.Demo{ID: 1})
	if po.DeletedAt.Valid || !po.DeletedAt.Time.IsZero() {
		t.Errorf("fromBiz 不应设置 deleted_at，实际：%+v", po.DeletedAt)
	}
}

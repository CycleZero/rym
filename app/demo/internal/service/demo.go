// Package service 是 demo 模块的传输适配层：proto 入参 / proto 出参。
//
// 职责与边界：
//   - 解析 proto 请求（校验交给 biz），每个 RPC 只调用一次 biz 用例
//   - biz 领域模型 → proto 的转换集中在 convert.go，并配严格对比测试
//   - 错误不在这里翻译成 HTTP 状态码：直接返回 errs.Error，
//     由 transport 层的 ErrorEncoder 统一渲染
package service

import (
	"context"
	"log/slog"

	v1 "gin-template/api/demo/v1"
	"gin-template/app/demo/internal/biz"
	"gin-template/pkg/common"
)

// DemoService 实现 v1.DemoServiceHTTPServer。
type DemoService struct {
	uc     *biz.Usecase
	logger *slog.Logger
}

// NewDemoService 由 Wire 注入用例与日志器。
func NewDemoService(uc *biz.Usecase, logger *slog.Logger) *DemoService {
	return &DemoService{uc: uc, logger: logger}
}

// CreateDemo 创建 Demo。
func (s *DemoService) CreateDemo(ctx context.Context, req *v1.CreateDemoRequest) (*v1.Demo, error) {
	demo, err := s.uc.Create(ctx, biz.CreateInput{
		Name:        req.GetName(),
		Description: req.GetDescription(),
		CreatedBy:   uint64(common.Metadata(ctx).UserID),
	})
	if err != nil {
		return nil, err
	}
	return toProto(demo), nil
}

// GetDemo 获取 Demo 详情。
func (s *DemoService) GetDemo(ctx context.Context, req *v1.GetDemoRequest) (*v1.Demo, error) {
	demo, err := s.uc.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return toProto(demo), nil
}

// ListDemos 分页获取 Demo 列表。
func (s *DemoService) ListDemos(ctx context.Context, req *v1.ListDemosRequest) (*v1.ListDemosResponse, error) {
	page, pageSize := s.uc.NormalizePage(int(req.GetPage()), int(req.GetPageSize()))

	demos, total, err := s.uc.List(ctx, page, pageSize)
	if err != nil {
		return nil, err
	}
	return &v1.ListDemosResponse{
		List:     toProtoList(demos),
		Total:    total,
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

// UpdateDemo 更新 Demo。
func (s *DemoService) UpdateDemo(ctx context.Context, req *v1.UpdateDemoRequest) (*v1.Demo, error) {
	demo, err := s.uc.Update(ctx, req.GetId(), biz.UpdateInput{
		Name:        req.GetName(),
		Description: req.GetDescription(),
	})
	if err != nil {
		return nil, err
	}
	return toProto(demo), nil
}

// DeleteDemo 删除 Demo。
func (s *DemoService) DeleteDemo(ctx context.Context, req *v1.DeleteDemoRequest) (*v1.DeleteDemoResponse, error) {
	if err := s.uc.Delete(ctx, req.GetId()); err != nil {
		return nil, err
	}
	return &v1.DeleteDemoResponse{Message: "删除成功"}, nil
}

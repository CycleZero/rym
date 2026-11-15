package server

import (
	"context"
	"slices"
	"testing"

	v1 "github.com/CycleZero/rym/api/demo/v1"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// fakeService 只用于路由注册测试：不依赖 biz/data。
type fakeService struct{}

func (fakeService) CreateDemo(context.Context, *v1.CreateDemoRequest) (*v1.Demo, error) {
	return nil, nil
}
func (fakeService) GetDemo(context.Context, *v1.GetDemoRequest) (*v1.Demo, error) {
	return nil, nil
}
func (fakeService) ListDemos(context.Context, *v1.ListDemosRequest) (*v1.ListDemosResponse, error) {
	return nil, nil
}
func (fakeService) UpdateDemo(context.Context, *v1.UpdateDemoRequest) (*v1.Demo, error) {
	return nil, nil
}
func (fakeService) DeleteDemo(context.Context, *v1.DeleteDemoRequest) (*v1.DeleteDemoResponse, error) {
	return nil, nil
}

// TestRoutesRegisteredFromProto 路由由 proto 生成：路径必须是 /v1/ 前缀，
// 且 5 个 RPC 全部注册（漏注册在编译期不会报错，靠这里兜底）。
func TestRoutesRegisteredFromProto(t *testing.T) {
	srv := khttp.NewServer()
	New(srv, fakeService{})

	var routes []string
	if err := srv.WalkRoute(func(r khttp.RouteInfo) error {
		routes = append(routes, r.Method+" "+r.Path)
		return nil
	}); err != nil {
		t.Fatalf("WalkRoute: %v", err)
	}

	want := []string{
		"POST /v1/demos",
		"GET /v1/demos/{id}",
		"GET /v1/demos",
		"PUT /v1/demos/{id}",
		"DELETE /v1/demos/{id}",
	}
	for _, w := range want {
		if !slices.Contains(routes, w) {
			t.Errorf("缺少路由 %q，实际注册：%v", w, routes)
		}
	}
}

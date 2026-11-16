// Package server 负责 IM 模块的**协议装配**：把生成的 HTTP 路由挂到共享
// 的 kratos http.Server 上。
//
// 只依赖 service（不认识 biz/data）；路由与路径全部来自 proto 生成物，
// 因此拆分微服务时本文件原样复用（换成自己的 server 实例即可）。
package server

import (
	v1 "github.com/CycleZero/rym/api/im/v1"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// Module 是 server 层返回值：仅用于让组合根感知"该服务的路由已注册"。
type Module struct {
	name string
}

// Name 返回服务模块名（启动日志用）。
func (m *Module) Name() string { return m.name }

// New 注册 IM 路由到共享 server。
//
// 依赖边 Module → *khttp.Server 由 Wire 保证：server 一定先于模块构造，
// 因此注册顺序不需要任何全局状态或 panic 校验。
func New(srv *khttp.Server, svc v1.ImServiceHTTPServer) *Module {
	v1.RegisterImServiceHTTPServer(srv, svc)
	return &Module{name: "im"}
}

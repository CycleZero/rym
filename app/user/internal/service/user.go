// Package service 是用户模块的传输适配层：proto 入参 / proto 出参。
//
// 职责与边界：
//   - 解析 proto 请求（校验交给 biz），每个 RPC 只调用一次 biz 用例
//   - 令牌签发在这里完成（biz 不碰密钥），错误由 transport 层统一渲染
//   - 密码哈希永不出现在响应里：转换函数只映射 User 的公开字段
package service

import (
	"context"
	"errors"
	"log/slog"

	v1 "github.com/CycleZero/rym/api/user/v1"
	"github.com/CycleZero/rym/app/user/internal/biz"
	"github.com/CycleZero/rym/pkg/common"
	"github.com/CycleZero/rym/pkg/errs"
	"github.com/CycleZero/rym/pkg/token"
)

// UserService 实现 v1.UserServiceHTTPServer。
type UserService struct {
	uc     *biz.Usecase
	issuer *token.Issuer
	logger *slog.Logger
}

// NewUserService 由 Wire 注入用例、令牌签发器与日志器。
func NewUserService(uc *biz.Usecase, issuer *token.Issuer, logger *slog.Logger) *UserService {
	return &UserService{uc: uc, issuer: issuer, logger: logger}
}

// Register 注册。
func (s *UserService) Register(ctx context.Context, req *v1.RegisterRequest) (*v1.User, error) {
	user, err := s.uc.Register(ctx, biz.RegisterInput{
		Username: req.GetUsername(),
		Password: req.GetPassword(),
		Nickname: req.GetNickname(),
	})
	if err != nil {
		return nil, err
	}
	return toProtoUser(user), nil
}

// Login 登录并签发令牌。
func (s *UserService) Login(ctx context.Context, req *v1.LoginRequest) (*v1.LoginResponse, error) {
	user, err := s.uc.Login(ctx, biz.LoginInput{
		Username: req.GetUsername(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, err
	}

	signed, expiresAt, err := s.issuer.Issue(user.ID, user.Username)
	if err != nil {
		return nil, errs.Internal("签发登录令牌失败").WithCause(err)
	}
	return &v1.LoginResponse{
		AccessToken: signed,
		TokenType:   "Bearer",
		ExpiresAt:   expiresAt.Unix(),
		User:        toProtoUser(user),
	}, nil
}

// GetMe 当前登录用户。
func (s *UserService) GetMe(ctx context.Context, _ *v1.GetMeRequest) (*v1.User, error) {
	// 身份由 transport 中间件写入 ctx；0 表示匿名。
	userID := uint64(common.Metadata(ctx).UserID)
	user, err := s.uc.GetByID(ctx, userID)
	if err != nil {
		// 令牌签名有效但用户已被删除：对客户端应表现为"登录失效"，
		// 而不是 404（否则带着有效令牌就能探测用户是否存在）。
		if errors.Is(err, biz.ErrUserNotFound) {
			return nil, errs.Unauthorized("登录状态已失效")
		}
		return nil, err
	}
	return toProtoUser(user), nil
}

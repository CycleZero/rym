// Package service 是 IM 模块的传输适配层：proto 入参 / proto 出参。
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

	v1 "github.com/CycleZero/rym/api/im/v1"
	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/pkg/common"
	"github.com/CycleZero/rym/pkg/errs"
)

// ImService 实现 v1.ImServiceHTTPServer。
type ImService struct {
	uc     *biz.Usecase
	logger *slog.Logger
}

// NewImService 由 Wire 注入用例与日志器。
func NewImService(uc *biz.Usecase, logger *slog.Logger) *ImService {
	return &ImService{uc: uc, logger: logger}
}

// SendMessage 发送消息。
func (s *ImService) SendMessage(ctx context.Context, req *v1.SendMessageRequest) (*v1.SendMessageResponse, error) {
	// 系统消息由服务端内部触发，不对客户端开放（否则任何人都能伪造"官方通知"）。
	if req.GetType() == v1.MessageType_MESSAGE_TYPE_SYSTEM {
		return nil, errs.InvalidArgument("系统消息不对客户端开放")
	}

	res, err := s.uc.SendMessage(ctx, biz.SendMessageInput{
		ConversationID: req.GetConversationId(),
		ToUserID:       req.GetToUserId(),
		SenderID:       uint64(common.Metadata(ctx).UserID),
		ClientMsgID:    req.GetClientMsgId(),
		Type:           biz.MessageType(req.GetType()),
		Body:           req.GetBody(),
		ReplyTo:        req.GetReplyTo(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.SendMessageResponse{
		Message:    toProtoMessage(res.Message),
		Duplicated: res.Duplicated,
		LastSeq:    res.LastSeq,
	}, nil
}

// PullMessages 拉取消息。
func (s *ImService) PullMessages(ctx context.Context, req *v1.PullMessagesRequest) (*v1.PullMessagesResponse, error) {
	res, err := s.uc.PullMessages(ctx, biz.PullMessagesInput{
		ConversationID: req.GetConversationId(),
		UserID:         uint64(common.Metadata(ctx).UserID),
		BeforeSeq:      req.GetBeforeSeq(),
		AfterSeq:       req.GetAfterSeq(),
		Limit:          int(req.GetLimit()),
	})
	if err != nil {
		return nil, err
	}
	return &v1.PullMessagesResponse{
		List:    toProtoMessageList(res.List),
		MaxSeq:  res.MaxSeq,
		HasMore: res.HasMore,
	}, nil
}

// ListConversations 会话列表。
func (s *ImService) ListConversations(ctx context.Context, req *v1.ListConversationsRequest) (*v1.ListConversationsResponse, error) {
	res, err := s.uc.ListConversations(ctx, biz.ListConversationsInput{
		UserID: uint64(common.Metadata(ctx).UserID),
		Limit:  int(req.GetLimit()),
	})
	if err != nil {
		return nil, err
	}
	return &v1.ListConversationsResponse{
		List:       toProtoConversationList(res.List),
		MaxVersion: res.MaxVersion,
	}, nil
}

// MarkRead 推进已读位。
func (s *ImService) MarkRead(ctx context.Context, req *v1.MarkReadRequest) (*v1.MarkReadResponse, error) {
	res, err := s.uc.MarkRead(ctx, biz.MarkReadInput{
		ConversationID: req.GetConversationId(),
		UserID:         uint64(common.Metadata(ctx).UserID),
		ReadSeq:        req.GetReadSeq(),
	})
	if err != nil {
		return nil, err
	}
	return &v1.MarkReadResponse{
		ReadSeq:     res.ReadSeq,
		UnreadCount: res.Unread,
		Version:     res.Version,
	}, nil
}

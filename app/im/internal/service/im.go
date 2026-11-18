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
	"time"

	v1 "github.com/CycleZero/rym/api/im/v1"
	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/pkg/common"
	"github.com/CycleZero/rym/pkg/errs"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// TicketIssuer 签发 WS 单次票据；生产实现是 pkg/wsticket.Store。
type TicketIssuer interface {
	Issue(ctx context.Context, uid uint64, origin string) (string, time.Time, error)
}

// ImService 实现 v1.ImServiceHTTPServer。
type ImService struct {
	uc      *biz.Usecase
	tickets TicketIssuer
	logger  *slog.Logger
}

// NewImService 由 Wire 注入用例、票据签发器与日志器。
func NewImService(uc *biz.Usecase, tickets TicketIssuer, logger *slog.Logger) *ImService {
	return &ImService{uc: uc, tickets: tickets, logger: logger}
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

// OpenSingleConversation 打开单聊会话。
func (s *ImService) OpenSingleConversation(ctx context.Context, req *v1.OpenSingleConversationRequest) (*v1.Conversation, error) {
	view, err := s.uc.OpenSingleConversation(ctx, uint64(common.Metadata(ctx).UserID), req.GetToUserId())
	if err != nil {
		return nil, err
	}
	return toProtoConversation(view), nil
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

// CreateWSTicket 签发 WS 握手票据。
func (s *ImService) CreateWSTicket(ctx context.Context, _ *v1.CreateWSTicketRequest) (*v1.CreateWSTicketResponse, error) {
	uid := uint64(common.Metadata(ctx).UserID)
	if uid == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	if s.tickets == nil {
		return nil, errs.Unavailable("WS 票据服务未启用")
	}

	origin := ""
	if r, ok := khttp.RequestFromServerContext(ctx); ok {
		origin = r.Header.Get("Origin")
	}
	ticket, expiresAt, err := s.tickets.Issue(ctx, uid, origin)
	if err != nil {
		return nil, errs.Internal("生成 WS 票据失败").WithCause(err)
	}
	return &v1.CreateWSTicketResponse{Ticket: ticket, ExpiresAt: expiresAt.Unix()}, nil
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

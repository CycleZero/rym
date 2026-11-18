// Package ws 把 IM 领域事件转换成 WS 推送帧。
//
// 只做“biz.Message -> JSON -> Hub.Publish”，不持有连接、不做重连；
// 连接生命周期在 pkg/transport/ws。
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/CycleZero/rym/app/im/internal/biz"
	transportws "github.com/CycleZero/rym/pkg/transport/ws"
)

// envelope 是所有服务端事件的统一外层；v 用于协议演进。
type envelope struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

type messageCreatedData struct {
	ConversationID uint64      `json:"conversation_id"`
	Message        messageView `json:"message"`
}

type messageView struct {
	MsgID          string `json:"msg_id"`
	ConversationID uint64 `json:"conversation_id"`
	Seq            int64  `json:"seq"`
	SenderID       uint64 `json:"sender_id"`
	ClientMsgID    string `json:"client_msg_id"`
	Type           int32  `json:"type"`
	Body           string `json:"body"`
	Status         int32  `json:"status"`
	ReplyTo        string `json:"reply_to,omitempty"`
	Version        int32  `json:"version"`
	CreatedAt      string `json:"created_at"`
}

type readUpdatedData struct {
	ConversationID uint64 `json:"conversation_id"`
	UserID         uint64 `json:"user_id"`
	ReadSeq        int64  `json:"read_seq"`
}

// Publisher 实现 biz.EventPublisher。
type Publisher struct {
	hub    *transportws.Hub
	logger *slog.Logger
}

// NewPublisher 由 Wire 注入 Hub 与日志器。
func NewPublisher(hub *transportws.Hub, logger *slog.Logger) biz.EventPublisher {
	return &Publisher{hub: hub, logger: logger}
}

// MessageCreated 向所有会话成员推送新消息。
//
// 推完整消息（与 HTTP 响应同构）：客户端若发现 seq 跳号，仍然走 after_seq 补洞。
func (p *Publisher) MessageCreated(ctx context.Context, conversationID uint64, msg *biz.Message, recipients []uint64) {
	if p == nil || p.hub == nil || msg == nil {
		return
	}
	raw, err := json.Marshal(envelope{
		V:    1,
		Type: "message.created",
		Data: messageCreatedData{
			ConversationID: conversationID,
			Message:        toMessageView(msg),
		},
	})
	if err != nil {
		p.logger.ErrorContext(ctx, "构建 message.created 事件失败", "error", err)
		return
	}
	for _, uid := range recipients {
		p.hub.Publish(uid, raw)
	}
}

// ReadUpdated 向所有会话成员推送已读位变化。
func (p *Publisher) ReadUpdated(ctx context.Context, conversationID, userID uint64, readSeq int64, recipients []uint64) {
	if p == nil || p.hub == nil {
		return
	}
	raw, err := json.Marshal(envelope{
		V:    1,
		Type: "read.updated",
		Data: readUpdatedData{
			ConversationID: conversationID,
			UserID:         userID,
			ReadSeq:        readSeq,
		},
	})
	if err != nil {
		p.logger.ErrorContext(ctx, "构建 read.updated 事件失败", "error", err)
		return
	}
	for _, uid := range recipients {
		p.hub.Publish(uid, raw)
	}
}

func toMessageView(m *biz.Message) messageView {
	return messageView{
		MsgID:          m.MsgID,
		ConversationID: m.ConversationID,
		Seq:            m.Seq,
		SenderID:       m.SenderID,
		ClientMsgID:    m.ClientMsgID,
		Type:           int32(m.Type),
		Body:           m.Body,
		Status:         m.Status,
		ReplyTo:        m.ReplyTo,
		Version:        m.Version,
		CreatedAt:      m.CreatedAt.Format(time.RFC3339),
	}
}

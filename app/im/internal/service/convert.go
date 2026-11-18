package service

import (
	"time"

	v1 "github.com/CycleZero/rym/api/im/v1"
	"github.com/CycleZero/rym/app/im/internal/biz"
)

// TimeLayout 对外时间格式：RFC3339（与 proto 注释一致）。
const TimeLayout = time.RFC3339

// toProtoMessage 把领域消息转换为 proto 契约。
//
// 逐字段映射：漏映射会被 convert_test.go 抓住。
func toProtoMessage(m *biz.Message) *v1.Message {
	if m == nil {
		return nil
	}
	out := &v1.Message{
		MsgId:          m.MsgID,
		ConversationId: m.ConversationID,
		Seq:            m.Seq,
		SenderId:       m.SenderID,
		ClientMsgId:    m.ClientMsgID,
		Type:           v1.MessageType(m.Type),
		Body:           m.Body,
		Status:         m.Status,
		ReplyTo:        m.ReplyTo,
		Version:        m.Version,
		CreatedAt:      formatTime(m.CreatedAt),
	}
	if m.EditedAt != nil {
		out.EditedAt = formatTime(*m.EditedAt)
	}
	return out
}

// toProtoMessageList 批量转换（保持非 nil 切片，避免 JSON 输出 null）。
func toProtoMessageList(list []*biz.Message) []*v1.Message {
	out := make([]*v1.Message, 0, len(list))
	for _, m := range list {
		out = append(out, toProtoMessage(m))
	}
	return out
}

// toProtoConversation 把会话摘要转换为 proto 契约。
func toProtoConversation(v *biz.ConversationView) *v1.Conversation {
	if v == nil {
		return nil
	}
	return &v1.Conversation{
		Id:          v.ID,
		Type:        v1.ConversationType(v.Type),
		LastSeq:     v.LastSeq,
		ReadSeq:     v.Participant.ReadSeq,
		UnreadCount: v.Unread,
		Version:     v.Participant.Version,
		LastMessage: toProtoMessage(v.LastMessage),
		Pinned:      v.Participant.Pinned,
		Muted:       v.Participant.Muted,
		JoinedAt:    formatTime(v.Participant.JoinedAt),
		PeerReadSeq: v.PeerReadSeq,
		PeerId:      v.PeerID,
	}
}

// toProtoConversationList 批量转换（保持非 nil 切片）。
func toProtoConversationList(list []*biz.ConversationView) []*v1.Conversation {
	out := make([]*v1.Conversation, 0, len(list))
	for _, v := range list {
		out = append(out, toProtoConversation(v))
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

package data

import (
	"time"

	"github.com/CycleZero/rym/app/im/internal/biz"
)

// toBiz 把会话 PO 转换为领域模型（逐字段 1:1）。
func (po *ConversationPO) toBiz() *biz.Conversation {
	if po == nil {
		return nil
	}
	out := &biz.Conversation{
		ID:               po.ID,
		Key:              po.ConvKey,
		Type:             biz.ConversationType(po.Type),
		LastSeq:          po.LastSeq,
		LastCountableSeq: po.LastCountableSeq,
		LastMsgID:        derefString(po.LastMsgID),
		CreatedAt:        po.CreatedAt,
		UpdatedAt:        po.UpdatedAt,
	}
	if po.LastMsgAt != nil {
		out.LastMsgAt = *po.LastMsgAt
	}
	return out
}

// conversationFromBiz 把领域模型转换为 PO。
func conversationFromBiz(c *biz.Conversation) *ConversationPO {
	return &ConversationPO{
		ID:               c.ID,
		ConvKey:          c.Key,
		Type:             int32(c.Type),
		LastSeq:          c.LastSeq,
		LastCountableSeq: c.LastCountableSeq,
		LastMsgID:        ptrString(c.LastMsgID),
		LastMsgAt:        ptrTime(c.LastMsgAt),
		CreatedAt:        c.CreatedAt,
		UpdatedAt:        c.UpdatedAt,
	}
}

// toBiz 把成员 PO 转换为领域模型。
func (po *ParticipantPO) toBiz() *biz.Participant {
	if po == nil {
		return nil
	}
	return &biz.Participant{
		ConversationID:   po.ConversationID,
		UserID:           po.UserID,
		ReadSeq:          po.ReadSeq,
		ReadCountableSeq: po.ReadCountableSeq,
		ReadSenderSeq:    po.ReadSenderSeq,
		SentSeq:          po.SentSeq,
		LastDeliveredSeq: po.LastDeliveredSeq,
		VisibleFromSeq:   po.VisibleFromSeq,
		Pinned:           po.Pinned,
		Muted:            po.Muted,
		Version:          po.Version,
		JoinedAt:         po.JoinedAt,
		LeftAt:           po.LeftAt,
	}
}

// toBiz 把消息 PO 转换为领域模型。
func (po *MessagePO) toBiz() *biz.Message {
	if po == nil {
		return nil
	}
	out := &biz.Message{
		MsgID:          po.MsgID,
		Seq:            po.Seq,
		ConversationID: po.ConversationID,
		SenderID:       po.SenderID,
		ClientMsgID:    po.ClientMsgID,
		Type:           biz.MessageType(po.Type),
		Body:           po.Body,
		CountableSeq:   po.CountableSeq,
		SenderSeq:      po.SenderSeq,
		Status:         po.Status,
		ReplyTo:        derefString(po.ReplyTo),
		Version:        po.Version,
		CreatedAt:      po.CreatedAt,
		EditedAt:       po.EditedAt,
	}
	return out
}

// messageFromBiz 把领域模型转换为 PO。
func messageFromBiz(m *biz.Message) *MessagePO {
	return &MessagePO{
		MsgID:          m.MsgID,
		ConversationID: m.ConversationID,
		Seq:            m.Seq,
		SenderID:       m.SenderID,
		ClientMsgID:    m.ClientMsgID,
		Type:           int32(m.Type),
		Body:           m.Body,
		CountableSeq:   m.CountableSeq,
		SenderSeq:      m.SenderSeq,
		Status:         m.Status,
		ReplyTo:        ptrString(m.ReplyTo),
		Version:        m.Version,
		CreatedAt:      m.CreatedAt,
		EditedAt:       m.EditedAt,
	}
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func ptrString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// ptrTime 零值时间转 NULL（SQLite / MySQL 都用 NULL 表示"还没有消息"）。
func ptrTime(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	return &v
}

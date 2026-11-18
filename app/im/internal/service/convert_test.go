package service

import (
	"testing"
	"time"

	v1 "github.com/CycleZero/rym/api/im/v1"
	"github.com/CycleZero/rym/app/im/internal/biz"
)

// TestToProtoMessageMapsEveryField 锁定 biz → proto 的消息映射完整性。
func TestToProtoMessageMapsEveryField(t *testing.T) {
	created := time.Date(2026, 11, 16, 8, 9, 10, 0, time.UTC)
	edited := created.Add(time.Minute)

	got := toProtoMessage(&biz.Message{
		MsgID:          "0123456789abcdef0123456789abcdef",
		ConversationID: 7,
		Seq:            42,
		SenderID:       9,
		ClientMsgID:    "c-1",
		Type:           biz.MessageTypeText,
		Body:           `{"text":"hi"}`,
		Status:         1,
		ReplyTo:        "fedcba9876543210fedcba9876543210",
		Version:        2,
		CreatedAt:      created,
		EditedAt:       &edited,
	})

	if got.GetMsgId() != "0123456789abcdef0123456789abcdef" ||
		got.GetConversationId() != 7 ||
		got.GetSeq() != 42 ||
		got.GetSenderId() != 9 ||
		got.GetClientMsgId() != "c-1" ||
		got.GetType() != v1.MessageType_MESSAGE_TYPE_TEXT ||
		got.GetBody() != `{"text":"hi"}` ||
		got.GetStatus() != 1 ||
		got.GetReplyTo() != "fedcba9876543210fedcba9876543210" ||
		got.GetVersion() != 2 ||
		got.GetCreatedAt() != "2026-11-16T08:09:10Z" ||
		got.GetEditedAt() != "2026-11-16T08:10:10Z" {
		t.Fatalf("消息字段映射不完整：%+v", got)
	}
}

// TestToProtoConversationMapsSummary 锁定会话摘要映射。
func TestToProtoConversationMapsSummary(t *testing.T) {
	joined := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	got := toProtoConversation(&biz.ConversationView{
		Conversation: biz.Conversation{
			ID:      1,
			Type:    biz.ConversationTypeSingle,
			LastSeq: 10,
		},
		Participant: biz.Participant{
			ReadSeq:  8,
			Pinned:   true,
			Muted:    true,
			Version:  3,
			JoinedAt: joined,
		},
		LastMessage: &biz.Message{MsgID: "m", Seq: 10},
		Unread:      2,
		PeerReadSeq: 7,
		PeerID:      2,
	})

	if got.GetId() != 1 ||
		got.GetType() != v1.ConversationType_CONVERSATION_TYPE_SINGLE ||
		got.GetLastSeq() != 10 ||
		got.GetReadSeq() != 8 ||
		got.GetUnreadCount() != 2 ||
		got.GetVersion() != 3 ||
		got.GetPeerReadSeq() != 7 ||
		got.GetPeerId() != 2 ||
		!got.GetPinned() || !got.GetMuted() ||
		got.GetJoinedAt() != "2026-11-16T08:00:00Z" ||
		got.GetLastMessage().GetSeq() != 10 {
		t.Fatalf("会话字段映射不完整：%+v", got)
	}
}

func TestToProtoNil(t *testing.T) {
	if got := toProtoMessage(nil); got != nil {
		t.Fatalf("nil 消息应返回 nil，实际 %+v", got)
	}
	if got := toProtoConversation(nil); got != nil {
		t.Fatalf("nil 会话应返回 nil，实际 %+v", got)
	}
	if got := toProtoMessageList(nil); got == nil {
		t.Fatal("空消息列表应返回非 nil 切片")
	}
	if got := toProtoConversationList(nil); got == nil {
		t.Fatal("空会话列表应返回非 nil 切片")
	}
}

// TestToProtoZeroTime 零值时间输出空串，避免把 0001-01-01 暴露给客户端。
func TestToProtoZeroTime(t *testing.T) {
	got := toProtoMessage(&biz.Message{MsgID: "m"})
	if got.GetCreatedAt() != "" || got.GetEditedAt() != "" {
		t.Fatalf("零值时间应输出空串，实际 created=%q edited=%q", got.GetCreatedAt(), got.GetEditedAt())
	}
}

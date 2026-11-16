package data

import (
	"time"
)

// ConversationPO 是 conversations 表的数据库模型（PO）。
//
// 表结构以 app/im/migrations/001_init_mysql.sql 为准，SQLite 版逻辑等价。
// 生产代码不做自动迁移，GORM 标签只用于列名映射。
type ConversationPO struct {
	ID               uint64 `gorm:"primaryKey;autoIncrement"`
	ConvKey          string
	Type             int32
	LastSeq          int64
	LastCountableSeq int64
	LastMsgID        *string
	LastMsgAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TableName 固定表名，避免依赖 GORM 的复数化规则。
func (ConversationPO) TableName() string { return "conversations" }

// ParticipantPO 是 conversation_participants 表的数据库模型（PO）。
type ParticipantPO struct {
	ConversationID   uint64 `gorm:"primaryKey"`
	UserID           uint64 `gorm:"primaryKey"`
	ReadSeq          int64
	ReadCountableSeq int64
	ReadSenderSeq    int64
	SentSeq          int64
	LastDeliveredSeq int64
	VisibleFromSeq   int64
	Pinned           bool
	Muted            bool
	Version          int64
	JoinedAt         time.Time
	LeftAt           *time.Time
}

// TableName 固定表名。
func (ParticipantPO) TableName() string { return "conversation_participants" }

// MessagePO 是 messages 表的数据库模型（PO）。
type MessagePO struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	MsgID          string
	ConversationID uint64
	Seq            int64
	SenderID       uint64
	ClientMsgID    string
	Type           int32
	Body           string
	BodyText       *string
	CountableSeq   int64
	SenderSeq      int64
	Status         int32
	ReplyTo        *string
	Version        int32
	CreatedAt      time.Time
	EditedAt       *time.Time
}

// TableName 固定表名。
func (MessagePO) TableName() string { return "messages" }

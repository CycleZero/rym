// Package biz 是 IM 模块的业务层（领域模型 + 仓储接口 + 用例）。
//
// 约束（与 app/demo 一致，CI 由 pkg/architecture 锁死）：
//   - 零 proto 依赖：对外契约转换在 service 层
//   - 不出现 *gorm.DB / *sql.Tx：事务边界通过 tx.TxManager 表达
//   - 仓储接口定义在这里（依赖倒置），实现由 data 层提供
//
// IM 的四条不变量（详见 docs/im-design-best-practices.md §1/§4）：
//  1. 一条消息不能丢：先落库再返回，不做"先 ACK 后落库"
//  2. 同一会话内不能乱序：seq 由服务端分配，不信任客户端时间
//  3. 客户端能判断缺了哪条：seq 在会话内连续，靠跳号发现漏消息
//  4. 重发必须幂等：client_msg_id 唯一键，返回首次分配的 seq
package biz

import (
	"context"
	"time"

	"github.com/CycleZero/rym/pkg/errs"
)

// 对外错误。都不携带"会话是否存在"的细节：非成员和不存在统一 403，
// 避免用 404/403 的差异探测某个会话 id 是否存在。
var (
	// ErrNotMember 无权访问该会话（未登录由 service 层拦成 401）。
	ErrNotMember = errs.Forbidden("无权访问该会话")

	// ErrConversationNotFound 仓储层的"查无此会话"，只在本包内部翻译，
	// 不会直接下发（见 ErrNotMember）。
	ErrConversationNotFound = errs.NotFound("会话不存在")
)

// ConversationType 会话类型。数值与 proto / 数据库保持一致。
type ConversationType int32

// 会话类型取值。
const (
	ConversationTypeSingle ConversationType = 1
	ConversationTypeGroup  ConversationType = 2
)

// MessageType 消息类型。数值与 proto / 数据库保持一致。
type MessageType int32

// 消息类型取值。
const (
	MessageTypeText   MessageType = 1
	MessageTypeImage  MessageType = 2
	MessageTypeFile   MessageType = 3
	MessageTypeAudio  MessageType = 4
	MessageTypeVideo  MessageType = 5
	MessageTypeSystem MessageType = 6
	MessageTypeCustom MessageType = 7
)

// Countable 该消息是否计入未读。
//
// 文档 §7.2：系统消息不计未读（客户端不可发送，v1 也不落消息表；
// 这里保留分支是为了将来服务端内部系统消息复用同一套计数逻辑）。
func (t MessageType) Countable() bool { return t != MessageTypeSystem }

// Valid 是否是已知类型。
func (t MessageType) Valid() bool { return t >= MessageTypeText && t <= MessageTypeCustom }

// Conversation 会话（共享的消息容器）。
type Conversation struct {
	ID   uint64
	Key  string
	Type ConversationType

	// LastSeq 会话发号游标：下一条消息的 seq = LastSeq + 1。
	LastSeq int64
	// LastCountableSeq 到最新一条为止"计未读"的累计条数（未读数水位）。
	LastCountableSeq int64

	LastMsgID string
	LastMsgAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Participant 会话成员的个人状态（每人一行）。
//
// 同一会话内每个人的已读/置顶/免打扰互不影响，所以不放在 Conversation 上。
type Participant struct {
	ConversationID uint64
	UserID         uint64

	// ReadSeq 已读游标，只增不减。
	ReadSeq int64
	// ReadCountableSeq / ReadSenderSeq 是 ReadSeq 当时的两个计数快照。
	// 未读 = (LastCountableSeq - ReadCountableSeq) - (SentSeq - ReadSenderSeq)，
	// 任一刻都能由水位重算，所以计数器错了可以自愈。
	ReadCountableSeq int64
	ReadSenderSeq    int64

	// SentSeq 我在本会话发过多少条（= 我最后一条消息的 sender_seq）。
	SentSeq int64

	LastDeliveredSeq int64
	VisibleFromSeq   int64

	Pinned  bool
	Muted   bool
	Version int64

	JoinedAt time.Time
	LeftAt   *time.Time
}

// Message 一条消息。
type Message struct {
	// MsgID 全局唯一、无顺序语义（UUIDv7 hex）。
	MsgID string
	// Seq 会话内连续序号（服务端分配）。
	Seq            int64
	ConversationID uint64
	SenderID       uint64
	ClientMsgID    string
	Type           MessageType
	Body           string
	// CountableSeq 到这条为止的计未读累计条数。
	CountableSeq int64
	// SenderSeq 发送者在本会话的第几条。
	SenderSeq int64
	Status    int32
	ReplyTo   string
	Version   int32
	CreatedAt time.Time
	EditedAt  *time.Time
}

// ConversationView 会话列表的一行：会话 + 我的状态 + 最后一条消息 + 未读。
type ConversationView struct {
	Conversation
	Participant Participant
	LastMessage *Message
	// Unread 未读数（由水位计算，见 Participant 注释）。
	Unread int64
	// PeerReadSeq 单聊对端的已读位；群聊为 0（v1 不做群已读聚合）。
	PeerReadSeq int64
}

// Settings IM 的业务参数；由组合根从服务配置转换（biz 不 import conf）。
type Settings struct {
	DefaultPageSize int
	MaxPageSize     int
	MaxBodyBytes    int
}

// Repo 定义 IM 的持久化能力。
//
// 事务语义：所有方法都必须用 tx.WithContext(ctx, db) 访问数据库，
// 这样 biz 开的 WithTx 会被 data 层透明复用。
type Repo interface {
	// EnsureSingleConversation 保证 (userA, userB) 的单聊会话存在并返回。
	//
	// 返回值**已持有该会话的行锁**（MySQL：SELECT ... FOR UPDATE），
	// 调用方可以安全地在同一事务里读-改-写会话进度。
	// 会话不存在时 Data 层负责创建会话与两个 participant。
	EnsureSingleConversation(ctx context.Context, userA, userB uint64) (*Conversation, error)

	// LockConversation 按 id 取会话并加行锁；不存在返回 ErrConversationNotFound。
	//
	// 加行锁的目的：同一会话的发送者排队，保证"分配序 == 提交序"，
	// 否则会出现 T1 拿到 101 未提交、T2 拿到 102 先提交 → 101 永久拉不到。
	LockConversation(ctx context.Context, id uint64) (*Conversation, error)

	// GetConversation 按 id 取会话（只读，不加锁）；不存在返回 ErrConversationNotFound。
	GetConversation(ctx context.Context, id uint64) (*Conversation, error)

	// GetParticipant 取成员状态；非成员返回 nil, nil。
	GetParticipant(ctx context.Context, conversationID, userID uint64) (*Participant, error)

	// FindMessageByClientID 按幂等键查已存在的消息；不存在返回 nil, nil。
	FindMessageByClientID(ctx context.Context, conversationID, senderID uint64, clientMsgID string) (*Message, error)

	// CreateMessage 落库一条消息。
	CreateMessage(ctx context.Context, msg *Message) error

	// UpdateConversationProgress 写回 last_seq / last_countable_seq / 最后一条消息。
	UpdateConversationProgress(ctx context.Context, conv *Conversation) error

	// UpdateParticipantSentSeq 写回我在本会话的发消息条数。
	UpdateParticipantSentSeq(ctx context.Context, conversationID, userID uint64, sentSeq int64) error

	// ListMessages 游标查询：
	//   - afterSeq > 0：seq > afterSeq，升序
	//   - 否则：seq < beforeSeq（beforeSeq=0 时取最新），倒序取 limit 条
	//
	// 返回顺序由调用方整理；为了判断 has_more，最多返回 limit+1 条。
	ListMessages(ctx context.Context, conversationID uint64, beforeSeq, afterSeq int64, limit int) ([]*Message, error)

	// ListConversations 当前用户的会话摘要，按最后消息时间倒序。
	ListConversations(ctx context.Context, userID uint64, limit int) ([]*ConversationView, error)

	// CountableAt 返回 seq <= readSeq 的最后一条消息的 countable_seq；没有则 0。
	CountableAt(ctx context.Context, conversationID uint64, readSeq int64) (int64, error)

	// SenderSeqAt 返回我在 seq <= readSeq 的最后一条消息的 sender_seq；没有则 0。
	SenderSeqAt(ctx context.Context, conversationID, userID uint64, readSeq int64) (int64, error)

	// AdvanceRead 条件推进已读位（只增不减）。
	//
	// 返回 false 表示已有更新的已读位（并发上报），调用方应重新读取。
	AdvanceRead(ctx context.Context, conversationID, userID uint64, readSeq, readCountableSeq, readSenderSeq int64) (bool, error)
}

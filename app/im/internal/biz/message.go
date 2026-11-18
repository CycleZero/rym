package biz

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CycleZero/rym/pkg/errs"
)

// errDuplicateMessage 是内部哨兵：并发首次请求撞唯一键时，
// 需要回滚本事务（把已分配的 seq / sent_seq 一起撤掉），再返回已存在的那条。
var errDuplicateMessage = errors.New("im: duplicate client_msg_id")

// SendMessageInput 发送消息入参（biz 自有 DTO，不依赖 proto）。
type SendMessageInput struct {
	// ConversationID 已有会话 id；为 0 时用 ToUserID 派生单聊会话。
	ConversationID uint64
	ToUserID       uint64
	SenderID       uint64
	ClientMsgID    string
	Type           MessageType
	Body           string
	ReplyTo        string
}

// SendMessageResult 发送结果。
type SendMessageResult struct {
	Message *Message
	// Duplicated true 表示命中幂等键，返回的是首次落库的那条（seq 不变）。
	Duplicated bool
	LastSeq    int64
}

// SendMessage 发送一条消息。
//
// 事务内的顺序（每一步都对应文档里的一个坑）：
//  1. 取会话并**加行锁**：同一会话的写者排队，保证分配序 == 提交序
//  2. 查幂等键：命中就返回首次分配的那条，不再发号（不产生"重发空洞"）
//  3. last_seq + 1、last_countable_seq 按类型 +0/+1
//  4. 发送者 sent_seq + 1
//  5. 插入消息（(conversation_id, seq) 与幂等键都是唯一索引，冲突即失败）
//  6. 写回会话进度
//
// 简化说明（v1）：没有 MQ / outbox，消息落库即返回；在线推送留给将来的
// gateway。代价是"落库成功但对方不在线时不推送"，客户端靠拉取补洞兜底。
func (u *Usecase) SendMessage(ctx context.Context, in SendMessageInput) (*SendMessageResult, error) {
	// 一次请求读一次快照：即使中途配置被热更新，本次请求也只用同一版。
	settings := u.settings.Settings()
	if err := u.validateSend(in, settings); err != nil {
		return nil, err
	}

	var result *SendMessageResult
	err := u.tm.WithTx(ctx, func(txCtx context.Context) error {
		conv, err := u.conversationForSend(txCtx, in)
		if err != nil {
			return err
		}

		p, err := u.repo.GetParticipant(txCtx, conv.ID, in.SenderID)
		if err != nil {
			return wrapInternal("查询会话成员失败", err)
		}
		if err := ensureParticipant(p); err != nil {
			return err
		}

		// 幂等闸门放在发号之前：重发不占用 seq，避免出现永久空洞。
		existing, err := u.repo.FindMessageByClientID(txCtx, conv.ID, in.SenderID, in.ClientMsgID)
		if err != nil {
			return wrapInternal("查询幂等消息失败", err)
		}
		if existing != nil {
			result = &SendMessageResult{Message: existing, Duplicated: true, LastSeq: conv.LastSeq}
			return nil
		}

		msgID, err := newMsgID()
		if err != nil {
			return errs.Internal("生成消息 ID 失败").WithCause(err)
		}

		// 截到毫秒：MySQL DATETIME(3) 与 SQLite 的存储精度一致，
		// 避免"同一时刻"在两种库上比较结果不同。
		now := time.Now().Truncate(time.Millisecond)
		conv.LastSeq++
		if in.Type.Countable() {
			conv.LastCountableSeq++
		}
		conv.LastMsgID = msgID
		conv.LastMsgAt = now

		sentSeq := p.SentSeq + 1
		msg := &Message{
			MsgID:          msgID,
			Seq:            conv.LastSeq,
			ConversationID: conv.ID,
			SenderID:       in.SenderID,
			ClientMsgID:    in.ClientMsgID,
			Type:           in.Type,
			Body:           in.Body,
			CountableSeq:   conv.LastCountableSeq,
			SenderSeq:      sentSeq,
			Status:         0,
			ReplyTo:        in.ReplyTo,
			Version:        1,
			CreatedAt:      now,
		}

		if err := u.repo.UpdateConversationProgress(txCtx, conv); err != nil {
			return wrapInternal("更新会话进度失败", err)
		}
		if err := u.repo.UpdateParticipantSentSeq(txCtx, conv.ID, in.SenderID, sentSeq); err != nil {
			return wrapInternal("更新发送计数失败", err)
		}
		if err := u.repo.CreateMessage(txCtx, msg); err != nil {
			// 兜底：并发首次请求可能都通过了幂等查询，唯一索引会让其中一个失败。
			// 这里回查幂等键——查到就说明另一请求已成功，直接返回它的 seq。
			// 代价是这个事务已分配的 seq 会被回滚成空洞（文档 §4.2 允许空洞语义）。
			if dup, findErr := u.repo.FindMessageByClientID(txCtx, conv.ID, in.SenderID, in.ClientMsgID); findErr == nil && dup != nil {
				result = &SendMessageResult{Message: dup, Duplicated: true, LastSeq: conv.LastSeq}
				// 返回哨兵让事务回滚：本事务已经加过 last_seq / sent_seq，
				// 但消息没落库，提交会把这两个计数写脏。
				return errDuplicateMessage
			}
			return wrapInternal("写入消息失败", err)
		}

		result = &SendMessageResult{Message: msg, Duplicated: false, LastSeq: conv.LastSeq}
		return nil
	})
	if err != nil {
		if errors.Is(err, errDuplicateMessage) {
			return result, nil
		}
		return nil, err
	}
	// 事务已提交后再推送；推送失败只影响实时性，不影响消息正确性。
	if result != nil && !result.Duplicated {
		u.publishMessageCreated(ctx, result.Message)
	}
	return result, nil
}

// publishMessageCreated 在事务提交后按会话成员扇出实时事件。
//
// 拿不到成员列表时直接跳过：客户端仍可通过 after_seq 拉取补洞。
func (u *Usecase) publishMessageCreated(ctx context.Context, msg *Message) {
	if u.publisher == nil || msg == nil {
		return
	}
	ids, err := u.repo.ListParticipantIDs(ctx, msg.ConversationID)
	if err != nil {
		return
	}
	u.publisher.MessageCreated(ctx, msg.ConversationID, msg, ids)
}

// conversationForSend 解析目标会话。
//
//   - to_user_id 路径：走"派生单聊键 + 不存在则创建"，返回时已持有行锁
//   - conversation_id 路径：直接按 id 加锁；不存在与无权访问统一 403
func (u *Usecase) conversationForSend(ctx context.Context, in SendMessageInput) (*Conversation, error) {
	if in.ToUserID > 0 {
		conv, err := u.repo.EnsureSingleConversation(ctx, in.SenderID, in.ToUserID)
		if err != nil {
			return nil, wrapInternal("创建或查询单聊会话失败", err)
		}
		if conv == nil {
			return nil, errs.Internal("单聊会话装配失败")
		}
		return conv, nil
	}

	conv, err := u.repo.LockConversation(ctx, in.ConversationID)
	if err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return nil, ErrNotMember
		}
		return nil, wrapInternal("查询会话失败", err)
	}
	if conv == nil {
		return nil, ErrNotMember
	}
	return conv, nil
}

// validateSend 校验发送入参。
func (u *Usecase) validateSend(in SendMessageInput, settings Settings) error {
	if in.SenderID == 0 {
		return errs.Unauthorized("请先登录")
	}
	if (in.ConversationID == 0) == (in.ToUserID == 0) {
		return errs.InvalidArgument("conversation_id 与 to_user_id 必须且只能给一个")
	}
	if in.ToUserID > 0 && in.ToUserID == in.SenderID {
		return errs.InvalidArgument("不能给自己发消息")
	}

	id := in.ClientMsgID
	if strings.TrimSpace(id) == "" {
		return errs.InvalidArgument("client_msg_id 不能为空")
	}
	if len(id) > 64 {
		return errs.InvalidArgument("client_msg_id 不能超过 64 字节")
	}

	if !in.Type.Valid() {
		return errs.InvalidArgument("不支持的消息类型")
	}
	if strings.TrimSpace(in.Body) == "" {
		return errs.InvalidArgument("消息体不能为空")
	}
	if max := settings.MaxBodyBytes; max > 0 && len(in.Body) > max {
		return errs.InvalidArgument("消息体超过大小上限")
	}
	if !json.Valid([]byte(in.Body)) {
		return errs.InvalidArgument("消息体必须是合法 JSON")
	}
	// v1 只校验形态，不校验被引用消息是否同会话：回复目标只作为不透明 ID 回显，
	// 不参与可见性计算，避免为此多一次查询（将来做消息详情时再补）。
	if in.ReplyTo != "" && !validMsgID(in.ReplyTo) {
		return errs.InvalidArgument("reply_to 必须是消息 ID")
	}
	return nil
}

// validMsgID 校验 32 位 hex（UUIDv7 去连字符后的形态）。
func validMsgID(v string) bool {
	if len(v) != 32 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

// PullMessagesInput 拉消息入参。
type PullMessagesInput struct {
	ConversationID uint64
	UserID         uint64
	BeforeSeq      int64
	AfterSeq       int64
	Limit          int
}

// PullMessagesResult 拉消息结果。
type PullMessagesResult struct {
	List    []*Message
	MaxSeq  int64
	HasMore bool
}

// PullMessages 按游标拉消息。
//
// 两种模式（文档 §6.2/§6.3）：
//   - after_seq > 0：补洞，返回 seq > after_seq 的升序消息
//   - 否则：翻历史，返回 seq < before_seq 的最近 limit 条，再反转成升序
//
// 一律按 (conversation_id, seq) 唯一索引走游标，禁止 OFFSET：
// 深分页越翻越慢，且新消息插入会导致重复/跳过。
func (u *Usecase) PullMessages(ctx context.Context, in PullMessagesInput) (*PullMessagesResult, error) {
	if in.UserID == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	if in.ConversationID == 0 {
		return nil, errs.InvalidArgument("conversation_id 不能为空")
	}
	if in.BeforeSeq < 0 || in.AfterSeq < 0 {
		return nil, errs.InvalidArgument("seq 不能为负数")
	}
	if in.BeforeSeq > 0 && in.AfterSeq > 0 {
		return nil, errs.InvalidArgument("before_seq 与 after_seq 不能同时使用")
	}
	settings := u.settings.Settings()
	limit := normalizeLimit(settings, in.Limit)

	// 先查成员身份：非成员直接 403，不暴露会话是否存在。
	p, err := u.repo.GetParticipant(ctx, in.ConversationID, in.UserID)
	if err != nil {
		return nil, wrapInternal("查询会话成员失败", err)
	}
	if err := ensureParticipant(p); err != nil {
		return nil, err
	}

	conv, err := u.repo.GetConversation(ctx, in.ConversationID)
	if err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return nil, ErrNotMember
		}
		return nil, wrapInternal("查询会话失败", err)
	}

	beforeSeq := in.BeforeSeq
	if in.AfterSeq == 0 && beforeSeq == 0 {
		beforeSeq = conv.LastSeq + 1 // 从最新一条往前
	}

	list, err := u.repo.ListMessages(ctx, in.ConversationID, beforeSeq, in.AfterSeq, limit)
	if err != nil {
		return nil, wrapInternal("查询消息失败", err)
	}

	hasMore := len(list) > limit
	if hasMore {
		list = list[:limit]
	}
	if in.AfterSeq == 0 {
		// 仓储按 seq DESC 返回（为了取"最近 N 条"），对外统一升序，客户端可直接追加。
		reverse(list)
	}
	if list == nil {
		list = make([]*Message, 0)
	}
	// 三个读不在同一事务里：若在 GetConversation 之后、ListMessages 之前
	// 刚好进来一条新消息，返回的 list 会比 conv.LastSeq 新。以实际返回为准，
	// 保证 max_seq 与 list 自洽（客户端不会因为 max_seq 偏小而误判漏消息）。
	maxSeq := conv.LastSeq
	if n := len(list); n > 0 && list[n-1].Seq > maxSeq {
		maxSeq = list[n-1].Seq
	}
	return &PullMessagesResult{List: list, MaxSeq: maxSeq, HasMore: hasMore}, nil
}

// reverse 原地反转消息切片。
func reverse(list []*Message) {
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
}

package biz

import (
	"context"
	"errors"

	"github.com/CycleZero/rym/pkg/errs"
)

// ListConversationsInput 会话列表入参。
type ListConversationsInput struct {
	UserID uint64
	Limit  int
}

// ListConversationsResult 会话列表结果。
type ListConversationsResult struct {
	List []*ConversationView
	// MaxVersion 本次结果里的最大状态版本（诊断用；v1 不做增量同步）。
	MaxVersion int64
}

// ListConversations 当前用户的会话列表（摘要 + 未读），按最后消息时间倒序。
//
// 只返回摘要，不返回历史：加入 200 个群时全量拉历史是灾难（文档 §6.4）。
func (u *Usecase) ListConversations(ctx context.Context, in ListConversationsInput) (*ListConversationsResult, error) {
	if in.UserID == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	settings := u.settings.Settings()
	limit := normalizeLimit(settings, in.Limit)

	list, err := u.repo.ListConversations(ctx, in.UserID, limit)
	if err != nil {
		return nil, wrapInternal("查询会话列表失败", err)
	}

	var maxVersion int64
	for _, v := range list {
		if v == nil {
			continue
		}
		v.Unread = unreadOf(&v.Conversation, &v.Participant)
		if v.Participant.Version > maxVersion {
			maxVersion = v.Participant.Version
		}
	}
	if list == nil {
		list = make([]*ConversationView, 0)
	}
	return &ListConversationsResult{List: list, MaxVersion: maxVersion}, nil
}

// OpenSingleConversation 打开（必要时创建）与 toUserID 的单聊会话。
//
// 复用 EnsureSingleConversation：会话键由双方 uid 字典序派生，A→B 与 B→A
// 得到同一条，且不产生消息。客户端从搜索结果点进聊天时用它拿 conversation_id。
func (u *Usecase) OpenSingleConversation(ctx context.Context, userID, toUserID uint64) (*ConversationView, error) {
	if userID == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	if toUserID == 0 || toUserID == userID {
		return nil, errs.InvalidArgument("to_user_id 不合法")
	}

	var conv *Conversation
	err := u.tm.WithTx(ctx, func(txCtx context.Context) error {
		c, err := u.repo.EnsureSingleConversation(txCtx, userID, toUserID)
		if err != nil {
			return err
		}
		conv = c
		return nil
	})
	if err != nil {
		return nil, wrapInternal("打开单聊会话失败", err)
	}
	return &ConversationView{Conversation: *conv, PeerID: toUserID}, nil
}

// MarkReadInput 已读上报入参。
type MarkReadInput struct {
	ConversationID uint64
	UserID         uint64
	ReadSeq        int64
}

// MarkReadResult 已读推进结果。
type MarkReadResult struct {
	ReadSeq int64
	Unread  int64
	Version int64
}

// MarkRead 推进已读位：只增不减，乱序上报安全。
//
// 为什么是"声明式已读到 N"而不是"清空未读"：
//   - 重复上报、乱序上报（先 100 再 90）都不会让游标回退
//   - 多端场景下广播"已读到 N"也不会因为到达顺序不同而回退
//
// 为什么在事务里锁会话行：计算 read_countable_seq 快照时，
// 必须保证 last_seq / 消息集合不再变化，否则快照会和已读位错位。
func (u *Usecase) MarkRead(ctx context.Context, in MarkReadInput) (*MarkReadResult, error) {
	if in.UserID == 0 {
		return nil, errs.Unauthorized("请先登录")
	}
	if in.ConversationID == 0 {
		return nil, errs.InvalidArgument("conversation_id 不能为空")
	}
	if in.ReadSeq < 0 {
		return nil, errs.InvalidArgument("read_seq 不能为负数")
	}

	var result *MarkReadResult
	err := u.tm.WithTx(ctx, func(txCtx context.Context) error {
		conv, err := u.repo.LockConversation(txCtx, in.ConversationID)
		if err != nil {
			if errors.Is(err, ErrConversationNotFound) {
				return ErrNotMember
			}
			return wrapInternal("查询会话失败", err)
		}

		p, err := u.repo.GetParticipant(txCtx, conv.ID, in.UserID)
		if err != nil {
			return wrapInternal("查询会话成员失败", err)
		}
		if err := ensureParticipant(p); err != nil {
			return err
		}

		target := in.ReadSeq
		if target > conv.LastSeq {
			// 客户端可能乐观地把本地最大 seq 报上来（例如刚收到推送），
			// 超出服务端进度时按服务端进度截断，不报错。
			target = conv.LastSeq
		}
		if target <= p.ReadSeq {
			// 乱序/重复上报：空操作，返回当前权威状态。
			result = &MarkReadResult{ReadSeq: p.ReadSeq, Unread: unreadOf(conv, p), Version: p.Version}
			return nil
		}

		readCountable, err := u.repo.CountableAt(txCtx, conv.ID, target)
		if err != nil {
			return wrapInternal("查询未读水位失败", err)
		}
		readSender, err := u.repo.SenderSeqAt(txCtx, conv.ID, in.UserID, target)
		if err != nil {
			return wrapInternal("查询发送水位失败", err)
		}

		if _, err := u.repo.AdvanceRead(txCtx, conv.ID, in.UserID, target, readCountable, readSender); err != nil {
			return wrapInternal("更新已读位失败", err)
		}

		// 回读一次：并发上报时以库里最终值为准（条件更新保证不回退）。
		final, err := u.repo.GetParticipant(txCtx, conv.ID, in.UserID)
		if err != nil {
			return wrapInternal("回读会话成员失败", err)
		}
		if err := ensureParticipant(final); err != nil {
			return err
		}
		result = &MarkReadResult{
			ReadSeq: final.ReadSeq,
			Unread:  unreadOf(conv, final),
			Version: final.Version,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result != nil {
		u.publishReadUpdated(ctx, in.ConversationID, in.UserID, result.ReadSeq)
	}
	return result, nil
}

// publishReadUpdated 在已读提交后推送；对端与其他设备据此更新已读位。
func (u *Usecase) publishReadUpdated(ctx context.Context, conversationID, userID uint64, readSeq int64) {
	if u.publisher == nil {
		return
	}
	ids, err := u.repo.ListParticipantIDs(ctx, conversationID)
	if err != nil {
		return
	}
	u.publisher.ReadUpdated(ctx, conversationID, userID, readSeq, ids)
}

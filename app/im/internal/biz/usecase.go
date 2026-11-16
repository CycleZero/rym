package biz

import (
	"github.com/CycleZero/rym/pkg/errs"
	"github.com/CycleZero/rym/pkg/tx"
)

// Usecase IM 业务编排。
//
// 事务边界在这里控制：发消息的「读-发号-写消息」必须原子，
// 仓储层（data）用 tx.WithContext 透明复用同一个事务。
//
// 本层**不记错误日志**：错误统一由传输层的 ErrorEncoder 收口
// （pkg/response.EncodeError 只对 5xx 记 Error，4xx 不写日志）。
type Usecase struct {
	repo     Repo
	tm       tx.TxManager
	settings Settings
}

// NewUsecase 由 Wire 注入仓储接口、事务管理器与业务参数。
func NewUsecase(repo Repo, tm tx.TxManager, settings Settings) *Usecase {
	return &Usecase{repo: repo, tm: tm, settings: settings}
}

// NormalizeLimit 归一化分页条数：缺省用默认值，超上限截断。
func (u *Usecase) NormalizeLimit(limit int) int {
	if limit <= 0 {
		return u.settings.DefaultPageSize
	}
	if max := u.settings.MaxPageSize; max > 0 && limit > max {
		return max
	}
	return limit
}

// ensureParticipant 检查成员身份；非成员与"会话不存在"统一返回 ErrNotMember，
// 不泄漏会话是否存在。
func ensureParticipant(p *Participant) error {
	if p == nil {
		return ErrNotMember
	}
	return nil
}

// unreadOf 未读数 = (最新计数水位 - 已读时水位) - (我发的总数 - 已读时我发的数)。
//
// 为什么不能 max_seq - read_seq：会把系统消息、自己发的消息都算成未读。
// 为什么不会为负：水位只增不减，两项差值都 >= 0；仍做兜底避免脏数据下溢。
func unreadOf(conv *Conversation, p *Participant) int64 {
	if conv == nil || p == nil {
		return 0
	}
	unread := (conv.LastCountableSeq - p.ReadCountableSeq) - (p.SentSeq - p.ReadSenderSeq)
	if unread < 0 {
		return 0
	}
	return unread
}

// wrapInternal 把基础设施错误归一化为统一内部错误。
// 已经是 *errs.Error 的（含领域错误）原样返回，保留业务码与状态码。
func wrapInternal(message string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.Internal(message).WithCause(err)
}

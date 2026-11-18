package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CycleZero/rym/app/im/internal/biz"
	"github.com/CycleZero/rym/pkg/tx"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// repo 实现 biz.Repo。
//
// 所有数据库操作统一走 tx.WithContext(ctx, r.db)：ctx 里带事务时自动复用，
// 否则退回普通连接——biz 控制事务边界，data 层无需感知。
type repo struct {
	db *gorm.DB
}

// NewRepo 由 Wire 注入 *gorm.DB。
func NewRepo(db *gorm.DB) *repo {
	return &repo{db: db}
}

// rowLock 给 MySQL 的 SELECT 加 FOR UPDATE。
//
// 为什么需要按 dialect 分支：
//   - MySQL（生产）：REPEATABLE READ 下普通 SELECT 读的是事务开始时的快照，
//     并发发消息必须用当前读（FOR UPDATE）把同一会话的写者排队，
//     才能保证"分配序 == 提交序"。
//   - SQLite（仅本地测试）：没有行锁；所有写入本来就串行（测试里用单连接），
//     而且 SQLite 不支持 FOR UPDATE 语法。
//
// 这是数据层唯一的方言分支；DDL 的方言差异在 migrations 的两个文件里。
func rowLock(db *gorm.DB) *gorm.DB {
	if db.Dialector.Name() == "mysql" {
		return db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return db
}

// EnsureSingleConversation 保证单聊会话存在，并持有该会话行锁。
//
// conv_key 用「双方 uid 字典序拼接」而不是哈希：
// 可读、无碰撞、排查时一眼能看出是哪两个人；单聊 A→B 与 B→A 得到同一个键，
// 因此不需要先查"是否已有会话"。
//
// 为什么是「先幂等插入，再加锁回读」，而不是「先 SELECT 再 INSERT」：
// MySQL 默认 REPEATABLE READ，事务里的第一条普通 SELECT 会固定快照。
// 如果先普通 SELECT（没读到，因为对方事务还没提交），再去抢行锁，
// 拿到锁之后所有普通 SELECT 仍然读旧快照 —— 会看不到对方刚提交的会话/消息，
// 于是重复发号或误判"不是成员"。先写后当前读（FOR UPDATE）可以避开这个坑。
// 并发首次发送时唯一键会让一个 INSERT 变成 no-op，随后回读的就是库里那条。
func (r *repo) EnsureSingleConversation(ctx context.Context, userA, userB uint64) (*biz.Conversation, error) {
	if userA > userB {
		userA, userB = userB, userA
	}
	key := fmt.Sprintf("s:%d:%d", userA, userB)
	db := tx.WithContext(ctx, r.db)

	now := time.Now().Truncate(time.Millisecond)
	po := ConversationPO{
		ConvKey:   key,
		Type:      int32(biz.ConversationTypeSingle),
		CreatedAt: now,
		UpdatedAt: now,
	}
	// DoNothing 在 MySQL 生成 ON DUPLICATE KEY UPDATE id=id，
	// 在 SQLite 生成 ON CONFLICT DO NOTHING；语义都是"已存在就跳过"。
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&po).Error; err != nil {
		return nil, err
	}

	// 回读并加行锁：并发场景下以库里已有的那条为准（当前读，不是快照读）。
	var locked ConversationPO
	if err := rowLock(tx.WithContext(ctx, r.db)).Where("conv_key = ?", key).First(&locked).Error; err != nil {
		return nil, err
	}

	// 两个成员都必须在（幂等：已存在则跳过）。
	if err := r.ensureParticipant(db, locked.ID, userA); err != nil {
		return nil, err
	}
	if err := r.ensureParticipant(db, locked.ID, userB); err != nil {
		return nil, err
	}
	return locked.toBiz(), nil
}

// ensureParticipant 确保成员行存在。
//
// 为什么不用 upsert：participants 是复合主键，MySQL 驱动为 DoNothing 生成的
// "ON DUPLICATE KEY UPDATE <列>" 在复合主键下依赖 GORM 内部选择哪一列，
// 不够直白。这里先查后插，冲突再回查——实现简单，且调用方通常已持有会话行锁。
func (r *repo) ensureParticipant(db *gorm.DB, conversationID, userID uint64) error {
	var po ParticipantPO
	err := db.Where("conversation_id = ? AND user_id = ?", conversationID, userID).First(&po).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	po = ParticipantPO{
		ConversationID: conversationID,
		UserID:         userID,
		JoinedAt:       time.Now().Truncate(time.Millisecond),
	}
	if createErr := db.Create(&po).Error; createErr != nil {
		// 并发插入：回查确认已存在即可
		var again ParticipantPO
		if findErr := db.Where("conversation_id = ? AND user_id = ?", conversationID, userID).First(&again).Error; findErr == nil {
			return nil
		}
		return createErr
	}
	return nil
}

// LockConversation 取会话并加行锁；不存在返回领域错误。
func (r *repo) LockConversation(ctx context.Context, id uint64) (*biz.Conversation, error) {
	var po ConversationPO
	if err := rowLock(tx.WithContext(ctx, r.db)).First(&po, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// GetConversation 只读获取会话。
func (r *repo) GetConversation(ctx context.Context, id uint64) (*biz.Conversation, error) {
	var po ConversationPO
	if err := tx.WithContext(ctx, r.db).First(&po, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrConversationNotFound
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// GetParticipant 取成员状态；非成员（含已退出）返回 nil, nil。
func (r *repo) GetParticipant(ctx context.Context, conversationID, userID uint64) (*biz.Participant, error) {
	var po ParticipantPO
	err := tx.WithContext(ctx, r.db).
		Where("conversation_id = ? AND user_id = ? AND left_at IS NULL", conversationID, userID).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// FindMessageByClientID 按幂等键查消息；不存在返回 nil, nil。
func (r *repo) FindMessageByClientID(ctx context.Context, conversationID, senderID uint64, clientMsgID string) (*biz.Message, error) {
	var po MessagePO
	err := tx.WithContext(ctx, r.db).
		Where("conversation_id = ? AND sender_id = ? AND client_msg_id = ?", conversationID, senderID, clientMsgID).
		First(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return po.toBiz(), nil
}

// CreateMessage 落库一条消息。
func (r *repo) CreateMessage(ctx context.Context, msg *biz.Message) error {
	return tx.WithContext(ctx, r.db).Create(messageFromBiz(msg)).Error
}

// UpdateConversationProgress 写回发号游标与最后一条消息。
//
// 调用方已持有会话行锁（LockConversation / EnsureSingleConversation），
// 所以这里是直接覆盖，不需要再比较 last_seq。
func (r *repo) UpdateConversationProgress(ctx context.Context, conv *biz.Conversation) error {
	values := map[string]any{
		"last_seq":           conv.LastSeq,
		"last_countable_seq": conv.LastCountableSeq,
		"last_msg_id":        conv.LastMsgID,
		"last_msg_at":        conv.LastMsgAt,
		"updated_at":         conv.LastMsgAt,
	}
	return tx.WithContext(ctx, r.db).
		Model(&ConversationPO{}).
		Where("id = ?", conv.ID).
		Updates(values).Error
}

// UpdateParticipantSentSeq 写回"我在本会话发过多少条"。
func (r *repo) UpdateParticipantSentSeq(ctx context.Context, conversationID, userID uint64, sentSeq int64) error {
	return tx.WithContext(ctx, r.db).
		Model(&ParticipantPO{}).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Update("sent_seq", sentSeq).Error
}

// ListMessages 游标查询消息。
//
// limit+1 是为了让上层判断 has_more；返回顺序：
//   - afterSeq > 0：升序（补洞，从缺口往后拿）
//   - 否则：倒序（翻历史，先拿最近的 N 条）
func (r *repo) ListMessages(ctx context.Context, conversationID uint64, beforeSeq, afterSeq int64, limit int) ([]*biz.Message, error) {
	db := tx.WithContext(ctx, r.db).Model(&MessagePO{}).Where("conversation_id = ?", conversationID)

	order := "seq DESC"
	switch {
	case afterSeq > 0:
		db = db.Where("seq > ?", afterSeq)
		order = "seq ASC"
	case beforeSeq > 0:
		db = db.Where("seq < ?", beforeSeq)
	}

	var pos []MessagePO
	if err := db.Order(order).Limit(limit + 1).Find(&pos).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.Message, 0, len(pos))
	for i := range pos {
		out = append(out, pos[i].toBiz())
	}
	return out, nil
}

// conversationRow 是会话列表的连表查询结果（只在本文件内使用）。
type conversationRow struct {
	ConversationID   uint64     `gorm:"column:conversation_id"`
	UserID           uint64     `gorm:"column:user_id"`
	ReadSeq          int64      `gorm:"column:read_seq"`
	ReadCountableSeq int64      `gorm:"column:read_countable_seq"`
	ReadSenderSeq    int64      `gorm:"column:read_sender_seq"`
	SentSeq          int64      `gorm:"column:sent_seq"`
	LastDeliveredSeq int64      `gorm:"column:last_delivered_seq"`
	VisibleFromSeq   int64      `gorm:"column:visible_from_seq"`
	Pinned           bool       `gorm:"column:pinned"`
	Muted            bool       `gorm:"column:muted"`
	PVersion         int64      `gorm:"column:p_version"`
	JoinedAt         time.Time  `gorm:"column:joined_at"`
	LeftAt           *time.Time `gorm:"column:left_at"`

	ConvType         int32      `gorm:"column:conv_type"`
	LastSeq          int64      `gorm:"column:last_seq"`
	LastCountableSeq int64      `gorm:"column:last_countable_seq"`
	LastMsgID        *string    `gorm:"column:last_msg_id"`
	LastMsgAt        *time.Time `gorm:"column:last_msg_at"`

	MMsgID       *string    `gorm:"column:m_msg_id"`
	MSeq         *int64     `gorm:"column:m_seq"`
	MSenderID    *uint64    `gorm:"column:m_sender_id"`
	MClientMsgID *string    `gorm:"column:m_client_msg_id"`
	MType        *int32     `gorm:"column:m_type"`
	MBody        *string    `gorm:"column:m_body"`
	MStatus      *int32     `gorm:"column:m_status"`
	MReplyTo     *string    `gorm:"column:m_reply_to"`
	MVersion     *int32     `gorm:"column:m_version"`
	MCreatedAt   *time.Time `gorm:"column:m_created_at"`
	MEditedAt    *time.Time `gorm:"column:m_edited_at"`

	PeerReadSeq int64  `gorm:"column:peer_read_seq"`
	PeerID      uint64 `gorm:"column:peer_id"`
}

// ListConversations 返回当前用户的会话摘要，按最后消息时间倒序。
//
// 左连 messages 表按 (conversation_id, last_seq) 取最后一条：
// 会话列表只要摘要，不能扫历史（文档 §6.4）。
func (r *repo) ListConversations(ctx context.Context, userID uint64, limit int) ([]*biz.ConversationView, error) {
	var rows []conversationRow
	err := tx.WithContext(ctx, r.db).
		Table("conversation_participants AS p").
		Select(`p.conversation_id, p.user_id, p.read_seq, p.read_countable_seq, p.read_sender_seq,
			p.sent_seq, p.last_delivered_seq, p.visible_from_seq, p.pinned, p.muted,
			p.version AS p_version, p.joined_at, p.left_at,
			c.type AS conv_type, c.last_seq, c.last_countable_seq, c.last_msg_id, c.last_msg_at,
			m.msg_id AS m_msg_id, m.seq AS m_seq, m.sender_id AS m_sender_id,
			m.client_msg_id AS m_client_msg_id, m.type AS m_type, m.body AS m_body,
			m.status AS m_status, m.reply_to AS m_reply_to, m.version AS m_version,
			m.created_at AS m_created_at, m.edited_at AS m_edited_at,
			CASE WHEN c.type = 1 THEN (
				SELECT MAX(rp.read_seq) FROM conversation_participants AS rp
				WHERE rp.conversation_id = p.conversation_id AND rp.user_id <> p.user_id
			) ELSE 0 END AS peer_read_seq,
			CASE WHEN c.type = 1 THEN (
				SELECT rp2.user_id FROM conversation_participants AS rp2
				WHERE rp2.conversation_id = p.conversation_id AND rp2.user_id <> p.user_id
				LIMIT 1
			) ELSE 0 END AS peer_id`).
		Joins("JOIN conversations AS c ON c.id = p.conversation_id").
		Joins("LEFT JOIN messages AS m ON m.conversation_id = c.id AND m.seq = c.last_seq").
		Where("p.user_id = ? AND p.left_at IS NULL", userID).
		Order("c.last_msg_at DESC, c.id DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]*biz.ConversationView, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		view := &biz.ConversationView{
			Conversation: biz.Conversation{
				ID:               row.ConversationID,
				Type:             biz.ConversationType(row.ConvType),
				LastSeq:          row.LastSeq,
				LastCountableSeq: row.LastCountableSeq,
				LastMsgID:        derefString(row.LastMsgID),
			},
			Participant: biz.Participant{
				ConversationID:   row.ConversationID,
				UserID:           row.UserID,
				ReadSeq:          row.ReadSeq,
				ReadCountableSeq: row.ReadCountableSeq,
				ReadSenderSeq:    row.ReadSenderSeq,
				SentSeq:          row.SentSeq,
				LastDeliveredSeq: row.LastDeliveredSeq,
				VisibleFromSeq:   row.VisibleFromSeq,
				Pinned:           row.Pinned,
				Muted:            row.Muted,
				Version:          row.PVersion,
				JoinedAt:         row.JoinedAt,
				LeftAt:           row.LeftAt,
			},
		}
		if row.LastMsgAt != nil {
			view.LastMsgAt = *row.LastMsgAt
		}
		view.PeerReadSeq = row.PeerReadSeq
		view.PeerID = row.PeerID
		if row.MMsgID != nil {
			view.LastMessage = &biz.Message{
				MsgID:          *row.MMsgID,
				Seq:            derefInt64(row.MSeq),
				ConversationID: row.ConversationID,
				SenderID:       derefUint64(row.MSenderID),
				ClientMsgID:    derefString(row.MClientMsgID),
				Type:           biz.MessageType(derefInt32(row.MType)),
				Body:           derefString(row.MBody),
				Status:         derefInt32(row.MStatus),
				ReplyTo:        derefString(row.MReplyTo),
				Version:        derefInt32(row.MVersion),
				CreatedAt:      derefTime(row.MCreatedAt),
				EditedAt:       row.MEditedAt,
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// CountableAt 返回 seq <= readSeq 的最后一条消息的 countable_seq。
func (r *repo) CountableAt(ctx context.Context, conversationID uint64, readSeq int64) (int64, error) {
	var po MessagePO
	err := tx.WithContext(ctx, r.db).
		Select("countable_seq").
		Where("conversation_id = ? AND seq <= ?", conversationID, readSeq).
		Order("seq DESC").Limit(1).Take(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return po.CountableSeq, nil
}

// SenderSeqAt 返回我在 seq <= readSeq 的最后一条消息的 sender_seq。
func (r *repo) SenderSeqAt(ctx context.Context, conversationID, userID uint64, readSeq int64) (int64, error) {
	var po MessagePO
	err := tx.WithContext(ctx, r.db).
		Select("sender_seq").
		Where("conversation_id = ? AND sender_id = ? AND seq <= ?", conversationID, userID, readSeq).
		Order("seq DESC").Limit(1).Take(&po).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return po.SenderSeq, nil
}

// AdvanceRead 条件推进已读位。
//
// 用 WHERE read_seq < ? 而不是 GREATEST(...)：这是标准 SQL，
// MySQL 与 SQLite 行为一致，且天然幂等（重复/乱序上报不会回退）。
func (r *repo) AdvanceRead(ctx context.Context, conversationID, userID uint64, readSeq, readCountableSeq, readSenderSeq int64) (bool, error) {
	res := tx.WithContext(ctx, r.db).
		Model(&ParticipantPO{}).
		Where("conversation_id = ? AND user_id = ? AND read_seq < ?", conversationID, userID, readSeq).
		Updates(map[string]any{
			"read_seq":           readSeq,
			"read_countable_seq": readCountableSeq,
			"read_sender_seq":    readSenderSeq,
			"version":            gorm.Expr("version + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefUint64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefInt32(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func derefTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

// ListParticipantIDs 返回会话当前成员（未退出）。
func (r *repo) ListParticipantIDs(ctx context.Context, conversationID uint64) ([]uint64, error) {
	var ids []uint64
	err := tx.WithContext(ctx, r.db).
		Model(&ParticipantPO{}).
		Where("conversation_id = ? AND left_at IS NULL", conversationID).
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

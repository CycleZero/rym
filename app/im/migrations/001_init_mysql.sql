-- IM v1：会话 + 参与人 + 消息（单聊 + ≤200 人小群）。
--
-- DDL 基线见 docs/im-design-best-practices.md §16.3。相对文档草案的**全部改动**
-- 都在这里列清楚（改动都有明确理由，不是顺手改）：
--
--   1. conversations.last_countable_seq
--      未读数水位。文档 §7.2 的公式需要“读到哪儿时计数到多少”，
--      把最新值冗余在会话行上，ListConversations 不必逐会话扫 messages。
--   2. messages.countable_seq / sender_seq
--      文档 §7.2 明确要求“表结构要预留这两个字段”，否则以后精确未读要重算历史。
--      v1 两个都写：countable_seq = 到这条为止“该计未读”的累计条数，
--      sender_seq = 发送者在本会话的第几条（用于扣掉自己发的消息）。
--   3. conversation_participants.read_countable_seq / read_sender_seq
--      已读位对应的两个计数快照。未读 = (last_countable - read_countable)
--      - (sent_seq - read_sender_seq)，任一刻都能由水位重算（可自愈）。
--   4. conversation_participants.sent_seq
--      我在本会话发过多少条（= 我最后一条消息的 sender_seq）。
--   5. messages.reply_to 用 CHAR(32) 而不是 BIGINT
--      对外消息 ID 是 msg_id（UUIDv7 hex），引用回复必须能指回它；
--      内部自增 id 不对外暴露。
--   6. messages.body_text 建了但不写
--      给将来的全文搜索/通知预览留位；v1 不做搜索，写入侧保持 NULL。
--
-- 仍然保留、但 v1 不使用的列（扩展点，不提前实现）：
--   left_at / visible_from_seq（成员有效期与入群可见范围）
--   last_delivered_seq（已送达回执）、pinned / muted（个人设置）
--   status / version / edited_at（撤回、编辑）
--   conversations.type（1=单聊 2=群聊，将来群规模分流是 if 分支而不是重构）

CREATE TABLE IF NOT EXISTS `conversations` (
    `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `conv_key`            VARCHAR(64)     NOT NULL COMMENT '单聊: s:<uid小>:<uid大>；群聊: g:<gid>',
    `type`                TINYINT         NOT NULL COMMENT '1=单聊 2=群聊',
    `last_seq`            BIGINT          NOT NULL DEFAULT 0 COMMENT '发号游标（会话内连续）',
    `last_countable_seq`  BIGINT          NOT NULL DEFAULT 0 COMMENT '到最新一条为止的计未读条数',
    `last_msg_id`         CHAR(32)        NULL COMMENT '最后一条消息 msg_id',
    `last_msg_at`         DATETIME(3)     NULL COMMENT '最后一条消息时间',
    `created_at`          DATETIME(3)     NOT NULL,
    `updated_at`          DATETIME(3)     NOT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_conv_key` (`conv_key`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT ='会话（共享的消息容器）';

CREATE TABLE IF NOT EXISTS `conversation_participants` (
    `conversation_id`     BIGINT UNSIGNED NOT NULL,
    `user_id`             BIGINT UNSIGNED NOT NULL,
    `read_seq`            BIGINT          NOT NULL DEFAULT 0 COMMENT '已读游标，只增不减',
    `read_countable_seq`  BIGINT          NOT NULL DEFAULT 0 COMMENT '已读位对应的 countable_seq 快照',
    `read_sender_seq`     BIGINT          NOT NULL DEFAULT 0 COMMENT '已读位对应的“我发的条数”快照',
    `sent_seq`            BIGINT          NOT NULL DEFAULT 0 COMMENT '我在本会话发过的条数',
    `last_delivered_seq`  BIGINT          NOT NULL DEFAULT 0 COMMENT '已投递位（v1 不写，留给已送达回执）',
    `visible_from_seq`    BIGINT          NOT NULL DEFAULT 0 COMMENT '可见起点（v1 不写，留给入群可见范围）',
    `pinned`              TINYINT         NOT NULL DEFAULT 0,
    `muted`               TINYINT         NOT NULL DEFAULT 0,
    `version`             BIGINT          NOT NULL DEFAULT 0 COMMENT '会话状态版本，多端增量用',
    `joined_at`           DATETIME(3)     NOT NULL,
    `left_at`             DATETIME(3)     NULL,
    PRIMARY KEY (`conversation_id`, `user_id`),
    KEY `idx_user_version` (`user_id`, `version`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT ='会话成员个人状态（每人一行）';

CREATE TABLE IF NOT EXISTS `messages` (
    `id`              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `msg_id`          CHAR(32)        NOT NULL COMMENT 'UUIDv7 hex（全局唯一，无顺序语义）',
    `conversation_id` BIGINT UNSIGNED NOT NULL,
    `seq`             BIGINT          NOT NULL COMMENT '会话内序号（连续，靠行锁分配）',
    `sender_id`       BIGINT UNSIGNED NOT NULL,
    `client_msg_id`   CHAR(64)        NOT NULL COMMENT '客户端幂等键（重发复用）',
    `type`            TINYINT         NOT NULL COMMENT '1=text 2=image 3=file 4=audio 5=video 6=system 7=custom',
    `body`            JSON            NOT NULL,
    `body_text`       VARCHAR(2048)   NULL COMMENT '冗余纯文本，供搜索/通知预览（v1 不写）',
    `countable_seq`   BIGINT          NOT NULL DEFAULT 0 COMMENT '到这条为止的计未读累计条数',
    `sender_seq`      BIGINT          NOT NULL DEFAULT 0 COMMENT '发送者在本会话的第几条',
    `status`          TINYINT         NOT NULL DEFAULT 0 COMMENT '0=正常 1=撤回 2=删除 3=屏蔽',
    `reply_to`        CHAR(32)        NULL COMMENT '引用回复的 msg_id',
    `version`         INT             NOT NULL DEFAULT 1 COMMENT '编辑次数',
    `created_at`      DATETIME(3)     NOT NULL,
    `edited_at`       DATETIME(3)     NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_conv_seq` (`conversation_id`, `seq`),
    UNIQUE KEY `uk_conv_client` (`conversation_id`, `sender_id`, `client_msg_id`),
    UNIQUE KEY `uk_msg_id` (`msg_id`),
    KEY `idx_conv_sender_seq` (`conversation_id`, `sender_id`, `seq`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT ='消息（会话内只存一份，不做写扩散）';

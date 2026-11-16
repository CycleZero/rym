-- IM v1（SQLite 版，用于本地测试 / 单机试用）。
--
-- 与 001_init_mysql.sql **逻辑等价**，只换方言：
--   - AUTO_INCREMENT → INTEGER PRIMARY KEY AUTOINCREMENT
--   - DATETIME(3) → DATETIME（SQLite 无精度参数）
--   - JSON → TEXT（避免 NUMERIC 亲和性对 JSON 文本做意外转换）
--   - ENGINE / CHARSET / COMMENT / KEY 语法 → 独立 CREATE INDEX
--   - BIGINT UNSIGNED → INTEGER（SQLite 只有 64 位有符号整数）
--
-- 生产以 MySQL 为准；改表时必须两份一起改，并在 commit body 里说明。

CREATE TABLE IF NOT EXISTS conversations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    conv_key           TEXT    NOT NULL,
    type               INTEGER NOT NULL,
    last_seq           INTEGER NOT NULL DEFAULT 0,
    last_countable_seq INTEGER NOT NULL DEFAULT 0,
    last_msg_id        TEXT    NULL,
    last_msg_at        DATETIME NULL,
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_conv_key ON conversations (conv_key);

CREATE TABLE IF NOT EXISTS conversation_participants (
    conversation_id    INTEGER NOT NULL,
    user_id            INTEGER NOT NULL,
    read_seq           INTEGER NOT NULL DEFAULT 0,
    read_countable_seq INTEGER NOT NULL DEFAULT 0,
    read_sender_seq    INTEGER NOT NULL DEFAULT 0,
    sent_seq           INTEGER NOT NULL DEFAULT 0,
    last_delivered_seq INTEGER NOT NULL DEFAULT 0,
    visible_from_seq   INTEGER NOT NULL DEFAULT 0,
    pinned             INTEGER NOT NULL DEFAULT 0,
    muted              INTEGER NOT NULL DEFAULT 0,
    version            INTEGER NOT NULL DEFAULT 0,
    joined_at          DATETIME NOT NULL,
    left_at            DATETIME NULL,
    PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_user_version ON conversation_participants (user_id, version);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    msg_id          TEXT    NOT NULL,
    conversation_id INTEGER NOT NULL,
    seq             INTEGER NOT NULL,
    sender_id       INTEGER NOT NULL,
    client_msg_id   TEXT    NOT NULL,
    type            INTEGER NOT NULL,
    body            TEXT    NOT NULL,
    body_text       TEXT    NULL,
    countable_seq   INTEGER NOT NULL DEFAULT 0,
    sender_seq      INTEGER NOT NULL DEFAULT 0,
    status          INTEGER NOT NULL DEFAULT 0,
    reply_to        TEXT    NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME NOT NULL,
    edited_at       DATETIME NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_conv_seq ON messages (conversation_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS uk_conv_client ON messages (conversation_id, sender_id, client_msg_id);
CREATE UNIQUE INDEX IF NOT EXISTS uk_msg_id ON messages (msg_id);
CREATE INDEX IF NOT EXISTS idx_conv_sender_seq ON messages (conversation_id, sender_id, seq);

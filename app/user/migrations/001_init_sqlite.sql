-- 用户表（SQLite 版，用于本地测试）。
--
-- 与 001_init_mysql.sql 逻辑等价，只换方言：
--   - AUTO_INCREMENT → INTEGER PRIMARY KEY AUTOINCREMENT
--   - DATETIME(3) → DATETIME
--   - BIGINT UNSIGNED → INTEGER
--   - UNIQUE KEY 语法 → 独立 CREATE UNIQUE INDEX
--
-- 生产以 MySQL 为准；改表必须两份一起改。

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    nickname      TEXT    NOT NULL DEFAULT '',
    status        INTEGER NOT NULL DEFAULT 1,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_username ON users (username);

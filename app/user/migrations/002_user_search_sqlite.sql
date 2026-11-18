-- 用户发现：nickname 前缀搜索列与索引（SQLite 测试库）。

ALTER TABLE users ADD COLUMN nickname_lower TEXT NOT NULL DEFAULT '';
UPDATE users SET nickname_lower = LOWER(nickname);
CREATE INDEX IF NOT EXISTS idx_users_nickname_lower ON users (nickname_lower);

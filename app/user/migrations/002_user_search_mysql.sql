-- 用户发现：nickname 前缀搜索列与索引。
--
-- 用增量迁移而不是改 001：已经建过的库不会因为 CREATE TABLE IF NOT EXISTS
-- 而补列。make migrate 会记录已执行文件（schema_migrations），不会重复 ALTER。

ALTER TABLE `users`
    ADD COLUMN `nickname_lower` VARCHAR(64) NOT NULL DEFAULT '' COMMENT '昵称小写，用于前缀搜索' AFTER `nickname`,
    ADD KEY `idx_users_nickname_lower` (`nickname_lower`);

UPDATE `users` SET `nickname_lower` = LOWER(`nickname`);

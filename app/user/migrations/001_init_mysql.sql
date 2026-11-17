-- 用户表（v1：注册 / 登录）。
--
-- v1 范围与预留：
--   只存登录必需的字段；手机号/邮箱、头像、资料、最后登录时间等
--   属于后续资料模块，不提前加列（原则：宁可少一个功能，不要多一层抽象）。
--
-- password_hash 用 bcrypt（60 字符左右），VARCHAR(255) 留余量。
-- username 统一小写后写入，避免 MySQL 默认大小写不敏感、SQLite 大小写敏感
-- 造成 "Alice 与 alice 算不算同一个人" 的跨库差异。
-- status: 1=正常 0=禁用（禁用后登录返回 403，令牌仍由中间件解析，
--         但 GetMe 会因状态检查失败，属于 v1 的最小策略）。

CREATE TABLE IF NOT EXISTS `users` (
    `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `username`      VARCHAR(32)     NOT NULL COMMENT '登录名（统一小写）',
    `password_hash` VARCHAR(255)    NOT NULL COMMENT 'bcrypt 哈希，永不下发',
    `nickname`      VARCHAR(64)     NOT NULL DEFAULT '' COMMENT '昵称',
    `status`        TINYINT         NOT NULL DEFAULT 1 COMMENT '1=正常 0=禁用',
    `created_at`    DATETIME(3)     NOT NULL,
    `updated_at`    DATETIME(3)     NOT NULL,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_username` (`username`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT ='用户（v1 最小实现）';

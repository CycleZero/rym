-- demo 表：编号迁移，禁止 AutoMigrate。
-- 本地执行：mysql <db_name> < app/demo/migrations/001_init.sql

CREATE TABLE IF NOT EXISTS `demo` (
    `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
    `name`        VARCHAR(100)    NOT NULL COMMENT '名称',
    `description` TEXT            NULL COMMENT '描述',
    `status`      TINYINT         NOT NULL DEFAULT 1 COMMENT '状态 1=正常 0=禁用',
    `created_by`  BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '创建人ID',
    `created_at`  DATETIME(3)     NULL COMMENT '创建时间',
    `updated_at`  DATETIME(3)     NULL COMMENT '更新时间',
    `deleted_at`  DATETIME(3)     NULL COMMENT '软删除时间',
    PRIMARY KEY (`id`),
    KEY `idx_demo_deleted_at` (`deleted_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT ='demo 示例表';

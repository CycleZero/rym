package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gin-template/pkg/log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormLogger 把 GORM 日志接到项目统一 slog（级别由配置决定，生产默认 Warn）。
type gormLogger struct {
	level logger.LogLevel
}

func newGormLogger(debug bool) logger.Interface {
	level := logger.Warn
	if debug {
		level = logger.Info
	}
	return &gormLogger{level: level}
}

func (l *gormLogger) LogMode(level logger.LogLevel) logger.Interface {
	clone := *l
	clone.level = level
	return &clone
}

func (l *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Info {
		log.FromContext(ctx).Info(fmt.Sprintf(msg, args...))
	}
}

func (l *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Warn {
		log.FromContext(ctx).Warn(fmt.Sprintf(msg, args...))
	}
}

func (l *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	if l.level >= logger.Error {
		log.FromContext(ctx).Error(fmt.Sprintf(msg, args...))
	}
}

// Trace 记录慢 SQL 与错误；RecordNotFound 属正常业务分支，不当作错误。
func (l *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	entry := log.FromContext(ctx)

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		entry.Warn("sql 执行失败", "error", err, "sql", sql, "rows", rows, "cost_ms", elapsed.Milliseconds())
	case l.level >= logger.Info:
		sql, rows := fc()
		entry.Info("sql", "sql", sql, "rows", rows, "cost_ms", elapsed.Milliseconds())
	}
}

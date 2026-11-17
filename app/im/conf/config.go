package conf

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

// 默认值：配置缺省时的兜底（与 proto3 零值区分开）。
const (
	DefaultPageSize   = 20
	MaxPageSize       = 100
	DefaultBodyBytes  = 64 * 1024 // 64KB：足够文本/卡片，图片/文件只存引用
	DefaultConfigPath = "configs/im/config.yaml"
)

// Load 读取 IM 服务自有配置。
//
// 文件不存在时返回默认值而不是报错：单体阶段该服务没有必须落盘的业务配置，
// 配置项出现后再落地文件即可（拆分后应改为 fail-fast）。
func Load(paths ...string) (*Config, error) {
	path := DefaultConfigPath
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}

	if _, statErr := os.Stat(path); statErr != nil {
		// 文件缺失 → 使用默认值
		return Default(), nil
	}

	c := config.New(config.WithSource(file.NewSource(path)))
	if err := c.Load(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("加载 im 配置失败(%s): %w", path, err)
	}
	out := new(Config)
	if err := c.Scan(out); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("解析 im 配置失败(%s): %w", path, err)
	}
	_ = c.Close()
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// Default 返回带默认值的配置。
func Default() *Config {
	c := &Config{
		DefaultPageSize: DefaultPageSize,
		MaxPageSize:     MaxPageSize,
		MaxBodyBytes:    DefaultBodyBytes,
	}
	_ = c.Validate()
	return c
}

// Validate 校验并补齐默认值（幂等）。
func (x *Config) Validate() error {
	if x == nil {
		return errors.New("im 配置为空")
	}
	if x.DefaultPageSize == 0 {
		x.DefaultPageSize = DefaultPageSize
	}
	if x.MaxPageSize == 0 {
		x.MaxPageSize = MaxPageSize
	}
	if x.MaxBodyBytes == 0 {
		x.MaxBodyBytes = DefaultBodyBytes
	}
	if x.DefaultPageSize > x.MaxPageSize {
		return fmt.Errorf("im 配置非法: default_page_size(%d) > max_page_size(%d)",
			x.DefaultPageSize, x.MaxPageSize)
	}
	return nil
}

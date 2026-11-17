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
	DefaultPageSize = 10
	MaxPageSize     = 100
)

// 服务自有配置默认路径；拆分微服务后由该服务的 main 传入。
const DefaultPath = "configs/demo/config.yaml"

// Load 读取 demo 服务自有配置。
//
// 文件不存在时返回默认值而不是报错：单体阶段该服务没有额外业务配置，
// 配置项出现后再落地文件即可（拆分后应改为 fail-fast）。
func Load(paths ...string) (*Config, error) {
	path := DefaultPath
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}

	c := config.New(config.WithSource(file.NewSource(path)))
	if _, statErr := os.Stat(path); statErr != nil {
		// 文件缺失 → 使用默认值（单体阶段该服务没有额外业务配置）
		return Default(), nil
	}
	if err := c.Load(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("加载 demo 配置失败(%s): %w", path, err)
	}
	out := new(Config)
	if err := c.Scan(out); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("解析 demo 配置失败(%s): %w", path, err)
	}
	_ = c.Close()
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// Default 返回带默认值的配置。
func Default() *Config {
	c := &Config{DefaultPageSize: DefaultPageSize, MaxPageSize: MaxPageSize}
	_ = c.Validate()
	return c
}

// Validate 校验并补齐默认值（幂等）。
func (x *Config) Validate() error {
	if x == nil {
		return errors.New("demo 配置为空")
	}
	if x.DefaultPageSize == 0 {
		x.DefaultPageSize = DefaultPageSize
	}
	if x.MaxPageSize == 0 {
		x.MaxPageSize = MaxPageSize
	}
	if x.DefaultPageSize > x.MaxPageSize {
		return fmt.Errorf("demo 配置非法: default_page_size(%d) > max_page_size(%d)",
			x.DefaultPageSize, x.MaxPageSize)
	}
	return nil
}

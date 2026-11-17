package conf

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
	"golang.org/x/crypto/bcrypt"
)

// 默认值：配置缺省时的兜底（与 proto3 零值区分开）。
const (
	// DefaultTokenTTLSeconds 7 天。IM 客户端长期在线，令牌太短会频繁掉线；
	// 续期/刷新令牌属于 v2，v1 先给一个够用的固定有效期。
	DefaultTokenTTLSeconds uint32 = 7 * 24 * 60 * 60
	// DefaultBcryptCost bcrypt.DefaultCost（10），单次哈希约几十毫秒。
	DefaultBcryptCost uint32 = uint32(bcrypt.DefaultCost)
	// DefaultConfigPath 服务自有配置默认路径。
	DefaultConfigPath = "configs/user/config.yaml"
)

// Load 读取 user 服务自有配置。
//
// 文件不存在时返回默认值而不是报错：单体阶段该服务没有必须落盘的业务配置，
// 配置项出现后再落地文件即可（拆分后应改为 fail-fast）。
func Load(paths ...string) (*Config, error) {
	path := DefaultConfigPath
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}

	if _, statErr := os.Stat(path); statErr != nil {
		return Default(), nil
	}

	c := config.New(config.WithSource(file.NewSource(path)))
	if err := c.Load(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("加载 user 配置失败(%s): %w", path, err)
	}
	out := new(Config)
	if err := c.Scan(out); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("解析 user 配置失败(%s): %w", path, err)
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
		TokenTtlSeconds: DefaultTokenTTLSeconds,
		BcryptCost:      DefaultBcryptCost,
	}
	_ = c.Validate()
	return c
}

// Validate 校验并补齐默认值（幂等）。
func (x *Config) Validate() error {
	if x == nil {
		return errors.New("user 配置为空")
	}
	if x.TokenTtlSeconds == 0 {
		x.TokenTtlSeconds = DefaultTokenTTLSeconds
	}
	if x.BcryptCost == 0 {
		x.BcryptCost = DefaultBcryptCost
	}
	if x.BcryptCost < uint32(bcrypt.MinCost) || x.BcryptCost > uint32(bcrypt.MaxCost) {
		return fmt.Errorf("user 配置非法: bcrypt_cost 必须在 %d-%d 之间，当前为 %d",
			bcrypt.MinCost, bcrypt.MaxCost, x.BcryptCost)
	}
	return nil
}

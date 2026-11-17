// Package conf 负责**进程级共享配置**的加载与校验。
//
// 配置结构由 conf/common.proto 定义（make config 生成 common.pb.go）；
// 本文件只放加载流程与手写方法（DSN / Addr / Validate / 默认值）。
//
// 服务自有配置不放这里：每个服务在 app/<svc>/conf 下自包含声明，拆分微服务
// 时整目录搬走即可。
//
// 本包不保存全局配置：Load 读取并校验后返回 conf.Bootstrap，
// 由调用方显式注入（项目内通过 Wire 完成）。
package conf

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
)

const (
	// DefaultPath 默认配置文件路径。file source 既支持单文件，也支持目录。
	DefaultPath = "config.yaml"

	// EnvPrefix 环境变量前缀。APP_DB_HOST 会被映射为 key "DB_HOST"，
	// 供配置文件里的 ${DB_HOST:...} 占位符使用。
	EnvPrefix = "APP_"

	// DefaultEtcdPrefix 业务配置在 etcd 中的根前缀。
	DefaultEtcdPrefix = "rym"
	// DefaultEtcdEnv 配置环境；dev/staging/prod 共用 etcd 时靠它隔离。
	DefaultEtcdEnv = "dev"
	// DefaultEtcdDialTimeoutSeconds 连接 etcd 的超时。
	DefaultEtcdDialTimeoutSeconds = 3
)

// DSN 组装 go-sql-driver/mysql 连接串。
func (x *DB) DSN() string {
	if x == nil {
		return ""
	}
	addr := net.JoinHostPort(x.GetHost(), strconv.Itoa(int(x.GetPort())))
	return x.GetUser() + ":" + x.GetPassword() + "@tcp(" + addr + ")/" + x.GetDbName() +
		"?charset=utf8mb4&parseTime=True&loc=Local"
}

// Addr 返回 Redis 的 host:port。
func (x *Redis) Addr() string {
	if x == nil {
		return ""
	}
	return net.JoinHostPort(x.GetHost(), strconv.Itoa(int(x.GetPort())))
}

// Addr 返回 HTTP 监听地址。
func (x *HTTP) Addr() string {
	if x == nil {
		return ""
	}
	return net.JoinHostPort(x.GetHost(), strconv.Itoa(int(x.GetPort())))
}

// Validate 校验启动必需项，避免带着空地址/端口跑到运行期才报错。
//
// 安全相关配置 **fail-closed**：需要本地鉴权却没有密钥时启动失败，
// 不允许静默退化成"人人可访问"。
func (x *Bootstrap) Validate() error {
	if x == nil {
		return errors.New("配置校验失败: 配置为空")
	}
	if x.GetServer().GetHttp().GetPort() == 0 {
		return errors.New("配置校验失败: server.http.port 不能为 0")
	}
	if x.GetData().GetDb().GetHost() == "" || x.GetData().GetDb().GetDbName() == "" {
		return errors.New("配置校验失败: data.db.host / data.db.db_name 不能为空")
	}
	switch mode := x.GetLog().GetMode(); mode {
	case "dev", "prod":
	default:
		return fmt.Errorf("配置校验失败: log.mode 只能是 dev 或 prod，当前为 %q", mode)
	}

	sec := x.GetSecurity()
	if !sec.GetTrustGatewayIdentity() && sec.GetJwtSecret() == "" && sec.GetRequireAuth() {
		return errors.New("配置校验失败: security.require_auth=true 时必须配置 security.jwt_secret（或信任网关身份）")
	}

	if x.Etcd == nil {
		x.Etcd = &Etcd{}
	}
	if x.Etcd.Prefix == "" {
		x.Etcd.Prefix = DefaultEtcdPrefix
	}
	if x.Etcd.Env == "" {
		x.Etcd.Env = DefaultEtcdEnv
	}
	if x.Etcd.DialTimeoutSeconds == 0 {
		x.Etcd.DialTimeoutSeconds = DefaultEtcdDialTimeoutSeconds
	}
	return nil
}

// EndpointList 返回清洗后的 etcd 地址列表（去空格、跳过空项）。
func (x *Etcd) EndpointList() []string {
	if x == nil {
		return nil
	}
	out := make([]string, 0, len(x.GetEndpoints()))
	for _, raw := range x.GetEndpoints() {
		if ep := strings.TrimSpace(raw); ep != "" {
			out = append(out, ep)
		}
	}
	return out
}

// Source 是一次配置加载的来源句柄（底层 Kratos config 实例）。
//
// 进程退出前必须 Close 以停止各 source 的 watcher。
type Source struct {
	c config.Config
}

// Watch 为某个 key 注册热更新回调（key 为点分路径，如 data.db）。
func (s *Source) Watch(key string, o config.Observer) error {
	if s == nil || s.c == nil {
		return errors.New("conf: 配置来源未初始化")
	}
	return s.c.Watch(key, o)
}

// Close 停止所有 source 的 watcher，进程退出前调用一次即可。
func (s *Source) Close() error {
	if s == nil || s.c == nil {
		return nil
	}
	return s.c.Close()
}

// Load 读取配置、解码为 conf.Bootstrap 并校验，返回配置数据与来源句柄。
func Load(paths ...string) (*Bootstrap, *Source, error) {
	path := DefaultPath
	if len(paths) > 0 && paths[0] != "" {
		path = paths[0]
	}

	c := config.New(
		config.WithSource(
			file.NewSource(path),     // 默认值：版本库中的配置文件
			env.NewSource(EnvPrefix), // 覆盖：APP_ 前缀环境变量
		),
		// 让 ${KEY:default} 的解析结果按字面量推断类型（bool/int/float）。
		config.WithResolveActualTypes(true),
	)
	if err := c.Load(); err != nil {
		return nil, nil, fmt.Errorf("加载配置失败(%s): %w", path, err)
	}

	out := new(Bootstrap) // conf.pb.go 生成类型，Scan 走 protojson
	if err := c.Scan(out); err != nil {
		_ = c.Close()
		return nil, nil, fmt.Errorf("解析配置失败(%s): %w", path, err)
	}

	if err := out.Validate(); err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	return out, &Source{c: c}, nil
}

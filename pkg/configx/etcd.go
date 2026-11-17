package configx

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/wire"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// DefaultPrefix / DefaultEnv / DefaultConfigFile 是 key 约定的兜底值。
// 实际取值来自 conf.Etcd；这里只保证 Key() 被单独调用时行为可预期。
const (
	DefaultPrefix     = "rym"
	DefaultEnv        = "dev"
	DefaultConfigFile = "config.yaml"
)

// ErrNotFound 表示 etcd 上不存在该配置 key。
var ErrNotFound = errors.New("configx: 配置不存在")

// EtcdParams etcd 连接参数（由组合根从进程级配置转换，pkg 不依赖 conf 类型）。
type EtcdParams struct {
	Endpoints   []string
	DialTimeout time.Duration
}

// NewEtcdClient 建立共享 etcd 客户端。
//
// endpoints 为空返回 (nil, no-op cleanup, nil)：表示不启用 etcd，
// 服务退回本地文件。调用方必须处理 nil client（见 NewStore）。
// 一个进程只建一个 client，所有 scope 共用。
func NewEtcdClient(p EtcdParams) (*clientv3.Client, func(), error) {
	endpoints := make([]string, 0, len(p.Endpoints))
	for _, raw := range p.Endpoints {
		if ep := strings.TrimSpace(raw); ep != "" {
			endpoints = append(endpoints, ep)
		}
	}
	if len(endpoints) == 0 {
		return nil, func() {}, nil
	}

	timeout := p.DialTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: timeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("连接 etcd 失败: %w", err)
	}
	return client, func() { _ = client.Close() }, nil
}

// Key 返回一个服务的配置 key。
//
// 约定：<prefix>/<env>/configs/<svc>/config.yaml
// 例如 prefix=rym、env=dev、svc=im →
// rym/dev/configs/im/config.yaml
//
// 用 path.Join 而不是 filepath.Join：etcd key 是斜杠分隔的字节串，
// 不能被 Windows 的反斜杠规则改写。
func Key(prefix, env, svc, file string) string {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if env == "" {
		env = DefaultEnv
	}
	if file == "" {
		file = DefaultConfigFile
	}
	return path.Join(prefix, env, "configs", svc, file)
}

// EtcdSource 是一个 etcd key 的配置来源（整文档，不做字段级 merge）。
type EtcdSource struct {
	client *clientv3.Client
	key    string
}

// NewEtcdSource 创建来源；client 不能为 nil。
func NewEtcdSource(client *clientv3.Client, key string) *EtcdSource {
	return &EtcdSource{client: client, key: key}
}

// Load 读取整份文档与当前 revision。
func (s *EtcdSource) Load(ctx context.Context) ([]byte, int64, error) {
	resp, err := s.client.Get(ctx, s.key)
	if err != nil {
		return nil, 0, err
	}
	if len(resp.Kvs) == 0 {
		return nil, resp.Header.Revision, fmt.Errorf("%w: %s", ErrNotFound, s.key)
	}
	return resp.Kvs[0].Value, resp.Header.Revision, nil
}

// Watch 从 from+1 开始监听该 key 的 PUT/DELETE。
//
// 不使用 WithPrefix：一个 scope 就是一个文档，前缀扫描会把将来新增的
// 其他文件也合并进来，破坏“一次 PUT = 一次原子更新”的语义。
func (s *EtcdSource) Watch(ctx context.Context, from int64) (<-chan Event, error) {
	out := make(chan Event)
	wch := s.client.Watch(ctx, s.key, clientv3.WithRev(from+1))

	go func() {
		defer close(out)
		for resp := range wch {
			if err := resp.Err(); err != nil {
				select {
				case out <- Event{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			for _, ev := range resp.Events {
				item := Event{Revision: ev.Kv.ModRevision}
				switch ev.Type {
				case clientv3.EventTypePut:
					item.Raw = ev.Kv.Value
				case clientv3.EventTypeDelete:
					item.Deleted = true
				}
				select {
				case out <- item:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// ProviderSet 提供 *clientv3.Client + 关闭函数（由 Wire 调用）。
var ProviderSet = wire.NewSet(NewEtcdClient)

// Package configx 提供运行期业务配置的加载与热更新。
//
// 定位（刻意做小）：
//   - 只负责“一个 scope 一个文档”的加载、校验、原子切换；
//   - 不接管进程级基础设施配置（DB/Redis/监听端口/OTel 仍然启动时读一次）；
//   - 不保存全局配置：Store 由 Wire 注入，调用方每次请求读 Current()。
//
// 并发模型：写侧只有 watch goroutine，读到新文档后解码到**新对象**，
// 校验通过再 atomic 替换；读侧拿到的永远是一份完整快照，不会看到半更新。
package configx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// DefaultLoadTimeout 启动时读取配置的超时。
//
// 不能无限等：etcd 不可达时要在启动阶段失败，而不是卡住进程。
const DefaultLoadTimeout = 5 * time.Second

// Store 是一个 scope 的配置快照。
//
// T 通常是 proto 消息的指针（如 *conf.Config）。存入后**不得修改**：
// 更新一律解码新对象再替换，否则并发读会看到半更新。
type Store[T any] struct {
	v atomic.Value
}

// NewStore 用初始值创建快照（本地文件/代码默认值场景）。
func NewStore[T any](initial T) *Store[T] {
	s := &Store[T]{}
	s.v.Store(initial)
	return s
}

// Current 返回当前快照。调用方只读，不要修改返回对象。
func (s *Store[T]) Current() T {
	return s.v.Load().(T)
}

// Source 是一个配置来源。
type Source interface {
	// Load 返回整份文档与版本号（文件源可用 0）。
	Load(ctx context.Context) (raw []byte, revision int64, err error)

	// Watch 从 from+1 开始推送变更；ctx 取消时关闭 channel。
	Watch(ctx context.Context, from int64) (<-chan Event, error)
}

// Event 是一次配置变更。
type Event struct {
	Raw      []byte
	Revision int64
	Deleted  bool
	Err      error
}

// Options 描述一个 scope 的加载方式。
type Options[T any] struct {
	// Scope 仅用于日志/排查，通常就是 etcd key。
	Scope string
	// Source 配置来源，不能为空。
	Source Source
	// Decode 把原始文档解码为一份新配置，不能为空。
	Decode func(raw []byte) (T, error)
	// Validate 校验并补默认值；返回错误时保留旧配置。
	Validate func(T) error
	// Logger 为空时使用 slog.Default。
	Logger *slog.Logger
	// LoadTimeout 为空时使用 DefaultLoadTimeout。
	LoadTimeout time.Duration
}

// Load 读取一次配置并启动 watch。
//
// 启动语义是 fail-closed：来源不可达、文档不存在、解码或校验失败都返回错误，
// 由调用方中止启动。运行期语义是 keep-last-good：后续非法更新只记日志，
// 不替换当前快照（见 watch）。
func Load[T any](ctx context.Context, opts Options[T]) (*Store[T], error) {
	if opts.Source == nil {
		return nil, errors.New("configx: source 不能为空")
	}
	if opts.Decode == nil {
		return nil, errors.New("configx: decode 不能为空")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	raw, rev, err := loadOnce(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("加载配置 %s 失败: %w", opts.Scope, err)
	}
	cfg, err := opts.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", opts.Scope, err)
	}
	if opts.Validate != nil {
		if err := opts.Validate(cfg); err != nil {
			return nil, fmt.Errorf("校验配置 %s 失败: %w", opts.Scope, err)
		}
	}

	store := NewStore(cfg)
	opts.Logger.InfoContext(ctx, "配置已加载", "scope", opts.Scope, "revision", rev)
	go store.watch(ctx, opts, rev, raw)
	return store, nil
}

// DecodeProto 把 YAML 文档严格解码为 proto 配置。
//
// 用 DiscardUnknown=false：字段名写错要显式失败，而不是静默忽略——
// 配置中心的错别字最难排查。滚动发布需要向前兼容时再单独放宽。
func DecodeProto[T proto.Message](raw []byte, newFn func() T) (T, error) {
	out := newFn()
	if any(out) == nil {
		return out, errors.New("configx: newFn 返回 nil")
	}

	var node map[string]any
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return out, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	if len(node) == 0 {
		return out, errors.New("配置文档为空")
	}

	js, err := json.Marshal(node)
	if err != nil {
		return out, fmt.Errorf("转换 JSON 失败: %w", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(js, out); err != nil {
		return out, fmt.Errorf("解析 proto 配置失败: %w", err)
	}
	return out, nil
}

// watch 常驻监听变更，直到 ctx 取消。
func (s *Store[T]) watch(ctx context.Context, opts Options[T], rev int64, initialRaw []byte) {
	last := append([]byte(nil), initialRaw...)

	apply := func(raw []byte, revision int64) {
		if bytes.Equal(raw, last) {
			return
		}
		cfg, err := opts.Decode(raw)
		if err != nil {
			opts.Logger.ErrorContext(ctx, "配置解析失败，保留上一版",
				"scope", opts.Scope, "revision", revision, "error", err)
			return
		}
		if opts.Validate != nil {
			if err := opts.Validate(cfg); err != nil {
				opts.Logger.ErrorContext(ctx, "配置校验失败，保留上一版",
					"scope", opts.Scope, "revision", revision, "error", err)
				return
			}
		}
		s.v.Store(cfg)
		last = append(last[:0], raw...)
		opts.Logger.InfoContext(ctx, "配置已热更新",
			"scope", opts.Scope, "revision", revision)
	}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		ch, err := opts.Source.Watch(ctx, rev)
		if err != nil {
			opts.Logger.ErrorContext(ctx, "订阅配置失败，将重试", "scope", opts.Scope, "error", err)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second

		for ev := range ch {
			if ev.Err != nil {
				opts.Logger.ErrorContext(ctx, "配置订阅中断，将全量重载", "scope", opts.Scope, "error", ev.Err)
				break
			}
			if ev.Revision > rev {
				rev = ev.Revision
			}
			if ev.Deleted {
				opts.Logger.WarnContext(ctx, "配置被删除，保留上一版",
					"scope", opts.Scope, "revision", ev.Revision)
				continue
			}
			apply(ev.Raw, ev.Revision)
		}
		if ctx.Err() != nil {
			return
		}

		// channel 关闭（订阅出错或连接重建）：全量重载一次，补上断开期间的事件。
		raw, r, err := loadOnce(ctx, opts)
		// 即使 Load 失败也推进 revision：否则 key 被删/压缩后会拿着旧 revision
		// 反复订阅到同一个错误，永远恢复不了。
		if r > rev {
			rev = r
		}
		if err != nil {
			opts.Logger.ErrorContext(ctx, "全量重载配置失败，保留上一版", "scope", opts.Scope, "error", err)
		} else {
			apply(raw, r)
		}

		if !sleep(ctx, time.Second) {
			return
		}
	}
}

func loadOnce[T any](ctx context.Context, opts Options[T]) ([]byte, int64, error) {
	timeout := opts.LoadTimeout
	if timeout <= 0 {
		timeout = DefaultLoadTimeout
	}
	loadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return opts.Source.Load(loadCtx)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

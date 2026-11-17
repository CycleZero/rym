package configx_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CycleZero/rym/conf"
	"github.com/CycleZero/rym/pkg/configx"

	"google.golang.org/protobuf/types/known/structpb"
)

// fakeSource 是内存配置源；测试不依赖 etcd。
type fakeSource struct {
	mu       sync.Mutex
	raw      []byte
	rev      int64
	failLoad error
	ch       chan configx.Event
}

func newFakeSource(raw string) *fakeSource {
	return &fakeSource{raw: []byte(raw), rev: 1, ch: make(chan configx.Event, 8)}
}

func (s *fakeSource) Load(context.Context) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failLoad != nil {
		return nil, s.rev, s.failLoad
	}
	if s.raw == nil {
		return nil, s.rev, configx.ErrNotFound
	}
	return append([]byte(nil), s.raw...), s.rev, nil
}

func (s *fakeSource) Watch(ctx context.Context, _ int64) (<-chan configx.Event, error) {
	out := make(chan configx.Event)
	go func() {
		defer close(out)
		for {
			select {
			case ev := <-s.ch:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (s *fakeSource) push(raw string, rev int64) {
	s.mu.Lock()
	s.raw = []byte(raw)
	s.rev = rev
	s.mu.Unlock()
	s.ch <- configx.Event{Raw: []byte(raw), Revision: rev}
}

func (s *fakeSource) remove(rev int64) {
	s.mu.Lock()
	s.raw = nil
	s.rev = rev
	s.mu.Unlock()
	s.ch <- configx.Event{Revision: rev, Deleted: true}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeStruct(raw []byte) (*structpb.Struct, error) {
	return configx.DecodeProto(raw, func() *structpb.Struct { return &structpb.Struct{} })
}

func validateX(s *structpb.Struct) error {
	if _, ok := s.Fields["x"]; !ok {
		return errors.New("缺少 x")
	}
	return nil
}

func xValue(s *structpb.Struct) float64 {
	if s == nil {
		return 0
	}
	return s.Fields["x"].GetNumberValue()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestLoadAndHotReload 合法更新生效；非法更新与删除保留上一版。
func TestLoadAndHotReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := newFakeSource("x: 1")
	store, err := configx.Load(ctx, configx.Options[*structpb.Struct]{
		Scope:    "test",
		Source:   src,
		Decode:   decodeStruct,
		Validate: validateX,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := xValue(store.Current()); got != 1 {
		t.Fatalf("初始值 x=%v，期望 1", got)
	}

	src.push("x: 2", 2)
	waitFor(t, func() bool { return xValue(store.Current()) == 2 })

	// 非法更新（缺 x）必须被拒绝，旧值保留。
	src.push("y: 1", 3)
	time.Sleep(50 * time.Millisecond)
	if got := xValue(store.Current()); got != 2 {
		t.Fatalf("非法更新后旧值应保留为 2，实际 %v", got)
	}

	// 删除 key 也保留旧值。
	src.remove(4)
	time.Sleep(50 * time.Millisecond)
	if got := xValue(store.Current()); got != 2 {
		t.Fatalf("key 删除后旧值应保留为 2，实际 %v", got)
	}

	// 恢复后继续热更新。
	src.push("x: 3", 5)
	waitFor(t, func() bool { return xValue(store.Current()) == 3 })
}

// TestLoadFailsClosed 启动阶段来源错误、非法文档、空文档都必须失败。
func TestLoadFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("来源错误", func(t *testing.T) {
		src := newFakeSource("x: 1")
		src.failLoad = errors.New("etcd 不可达")
		if _, err := configx.Load(ctx, configx.Options[*structpb.Struct]{
			Scope: "test", Source: src, Decode: decodeStruct, Validate: validateX, Logger: discardLogger(),
		}); err == nil {
			t.Fatal("来源错误应导致启动失败")
		}
	})

	t.Run("校验失败", func(t *testing.T) {
		src := newFakeSource("y: 1")
		if _, err := configx.Load(ctx, configx.Options[*structpb.Struct]{
			Scope: "test", Source: src, Decode: decodeStruct, Validate: validateX, Logger: discardLogger(),
		}); err == nil {
			t.Fatal("校验失败应导致启动失败")
		}
	})

	t.Run("空文档", func(t *testing.T) {
		src := newFakeSource("")
		if _, err := configx.Load(ctx, configx.Options[*structpb.Struct]{
			Scope: "test", Source: src, Decode: decodeStruct, Validate: validateX, Logger: discardLogger(),
		}); err == nil {
			t.Fatal("空文档应导致启动失败")
		}
	})
}

// TestDecodeProtoRejectsUnknownField 字段名写错必须报错，不能静默忽略。
func TestDecodeProtoRejectsUnknownField(t *testing.T) {
	if _, err := configx.DecodeProto([]byte("foo: 1\n"), func() *conf.Bootstrap { return &conf.Bootstrap{} }); err == nil {
		t.Fatal("未知字段应返回错误")
	}
}

// TestKey 锁定 etcd key 约定：<prefix>/<env>/configs/<svc>/config.yaml。
func TestKey(t *testing.T) {
	cases := []struct {
		prefix, env, svc, file string
		want                   string
	}{
		{"rym", "dev", "im", "", "rym/dev/configs/im/config.yaml"},
		{"rym", "prod", "im", "", "rym/prod/configs/im/config.yaml"},
		{"", "", "user", "", "rym/dev/configs/user/config.yaml"},
		{"/rym", "dev", "im", "feature.yaml", "/rym/dev/configs/im/feature.yaml"},
	}
	for _, tc := range cases {
		if got := configx.Key(tc.prefix, tc.env, tc.svc, tc.file); got != tc.want {
			t.Errorf("Key(%q,%q,%q,%q)=%q，期望 %q", tc.prefix, tc.env, tc.svc, tc.file, got, tc.want)
		}
	}
}

// TestNewEtcdClientEmpty endpoints 为空表示不启用 etcd，必须返回 nil client 而不是报错。
func TestNewEtcdClientEmpty(t *testing.T) {
	client, cleanup, err := configx.NewEtcdClient(configx.EtcdParams{Endpoints: []string{"  "}})
	if err != nil {
		t.Fatalf("空 endpoints 不应报错：%v", err)
	}
	if client != nil {
		t.Fatal("空 endpoints 应返回 nil client")
	}
	if cleanup == nil {
		t.Fatal("cleanup 不能为 nil")
	}
	cleanup()
}

// TestDecodeProtoRequiresNonEmpty 只有注释或空内容的文档要显式失败。
func TestDecodeProtoRequiresNonEmpty(t *testing.T) {
	_, err := configx.DecodeProto([]byte("# 只有注释\n"), func() *structpb.Struct { return &structpb.Struct{} })
	if err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空文档应报错，实际 %v", err)
	}
}

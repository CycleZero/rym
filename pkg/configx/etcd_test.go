package configx_test

// 真实 etcd 的冒烟测试：默认 skip，避免 CI / 本地测试依赖外部服务。
//
// 用法：
//   RYM_TEST_ETCD_ENDPOINTS=127.0.0.1:2379 go test ./pkg/configx -run TestEtcdSourceIntegration -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CycleZero/rym/pkg/configx"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestEtcdSourceIntegration(t *testing.T) {
	rawEndpoints := os.Getenv("RYM_TEST_ETCD_ENDPOINTS")
	if rawEndpoints == "" {
		t.Skip("设置 RYM_TEST_ETCD_ENDPOINTS 后运行（默认不连 etcd）")
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   strings.Split(rawEndpoints, ","),
		DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("连接 etcd 失败：%v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	key := configx.Key("rym-test", "dev", "im", "config.yaml")
	putCtx, cancelPut := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := client.Put(putCtx, key, "x: 1"); err != nil {
		cancelPut()
		t.Fatalf("写入测试配置失败：%v", err)
	}
	cancelPut()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Delete(ctx, key)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := configx.Load(ctx, configx.Options[*structpb.Struct]{
		Scope:    key,
		Source:   configx.NewEtcdSource(client, key),
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

	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := client.Put(updateCtx, key, "x: 2"); err != nil {
		cancelUpdate()
		t.Fatalf("更新测试配置失败：%v", err)
	}
	cancelUpdate()

	waitFor(t, func() bool { return xValue(store.Current()) == 2 })
}

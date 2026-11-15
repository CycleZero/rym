package biz_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/CycleZero/rym/app/demo/internal/biz"
	"github.com/CycleZero/rym/pkg/errs"
)

// fakeRepo 是零依赖的仓储替身（仅测试使用）。
type fakeRepo struct {
	mu      sync.Mutex
	items   map[uint64]*biz.Demo
	nextID  uint64
	gotPage int
	gotSize int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: make(map[uint64]*biz.Demo)}
}

func (r *fakeRepo) Create(_ context.Context, demo *biz.Demo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	demo.ID = r.nextID
	r.items[demo.ID] = demo
	return nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uint64) (*biz.Demo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.items[id]; ok {
		return d, nil
	}
	return nil, biz.ErrDemoNotFound
}

func (r *fakeRepo) List(_ context.Context, page, pageSize int) ([]*biz.Demo, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gotPage, r.gotSize = page, pageSize
	out := make([]*biz.Demo, 0, len(r.items))
	for _, d := range r.items {
		out = append(out, d)
	}
	return out, int64(len(out)), nil
}

func (r *fakeRepo) Update(_ context.Context, demo *biz.Demo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[demo.ID]; !ok {
		return biz.ErrDemoNotFound
	}
	r.items[demo.ID] = demo
	return nil
}

func (r *fakeRepo) Delete(_ context.Context, id uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return biz.ErrDemoNotFound
	}
	delete(r.items, id)
	return nil
}

// fakeTx 记录事务调用次数，并可注入失败。
type fakeTx struct {
	calls int
	fail  error
}

func (f *fakeTx) WithTx(ctx context.Context, fn func(context.Context) error) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	return fn(ctx)
}

func newUsecase(t *testing.T) (*biz.Usecase, *fakeRepo, *fakeTx) {
	t.Helper()
	repo := newFakeRepo()
	tx := &fakeTx{}
	uc := biz.NewUsecase(repo, tx, biz.Settings{DefaultPageSize: 10, MaxPageSize: 20})
	return uc, repo, tx
}

func TestCreateTrimsNameAndRunsInTx(t *testing.T) {
	uc, repo, tx := newUsecase(t)

	demo, err := uc.Create(context.Background(), biz.CreateInput{Name: "  demo  ", CreatedBy: 7})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if demo.Name != "demo" {
		t.Errorf("Name = %q, want demo（应去掉首尾空白）", demo.Name)
	}
	if demo.ID == 0 {
		t.Error("新建记录应回填 ID")
	}
	if tx.calls != 1 {
		t.Errorf("事务调用次数 = %d, want 1", tx.calls)
	}
	if _, err := repo.GetByID(context.Background(), demo.ID); err != nil {
		t.Errorf("记录应已写入仓储: %v", err)
	}
}

func TestCreateRejectsEmptyName(t *testing.T) {
	uc, _, tx := newUsecase(t)

	_, err := uc.Create(context.Background(), biz.CreateInput{Name: "   "})
	if status, code, _ := errs.Resolve(err); status != http.StatusBadRequest || code != errs.CodeInvalidArgument {
		t.Errorf("errs.Resolve = (%d,%d), want (400,%d)", status, code, errs.CodeInvalidArgument)
	}
	if tx.calls != 0 {
		t.Errorf("校验失败不应开启事务，实际调用 %d 次", tx.calls)
	}
}

// TestCreateRollsBackOnTxError 事务失败必须向上返回 5xx 领域错误，而不是静默成功。
func TestCreateRollsBackOnTxError(t *testing.T) {
	uc, repo, tx := newUsecase(t)
	tx.fail = errors.New("db down")

	_, err := uc.Create(context.Background(), biz.CreateInput{Name: "x"})
	if status, _, _ := errs.Resolve(err); status != http.StatusInternalServerError {
		t.Errorf("状态码 = %d, want 500", status)
	}
	if len(repo.items) != 0 {
		t.Error("事务失败时不应有记录写入")
	}
}

func TestListNormalizesPage(t *testing.T) {
	uc, repo, _ := newUsecase(t)

	cases := []struct {
		name               string
		page, size         int
		wantPage, wantSize int
	}{
		{"缺省值", 0, 0, 1, 10},
		{"负数", -3, -1, 1, 10},
		{"超上限截断", 2, 500, 2, 20},
		{"合法值", 3, 5, 3, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := uc.List(context.Background(), tc.page, tc.size); err != nil {
				t.Fatalf("List: %v", err)
			}
			if repo.gotPage != tc.wantPage || repo.gotSize != tc.wantSize {
				t.Errorf("repo 收到 (%d,%d), want (%d,%d)", repo.gotPage, repo.gotSize, tc.wantPage, tc.wantSize)
			}
		})
	}
}

func TestGetByIDNotFoundIsClientError(t *testing.T) {
	uc, _, _ := newUsecase(t)

	_, err := uc.GetByID(context.Background(), 404)
	if status, code, _ := errs.Resolve(err); status != http.StatusNotFound || code != errs.CodeNotFound {
		t.Errorf("errs.Resolve = (%d,%d), want (404,%d)", status, code, errs.CodeNotFound)
	}
}

func TestUpdateRunsInTx(t *testing.T) {
	uc, _, tx := newUsecase(t)
	created, err := uc.Create(context.Background(), biz.CreateInput{Name: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tx.calls = 0

	updated, err := uc.Update(context.Background(), created.ID, biz.UpdateInput{Name: "new", Description: "d"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "new" || updated.Description != "d" {
		t.Errorf("更新结果 = %+v", updated)
	}
	if tx.calls != 1 {
		t.Errorf("事务调用次数 = %d, want 1", tx.calls)
	}
}

func TestDeleteNotFound(t *testing.T) {
	uc, _, _ := newUsecase(t)

	err := uc.Delete(context.Background(), 999)
	if status, _, _ := errs.Resolve(err); status != http.StatusNotFound {
		t.Errorf("状态码 = %d, want 404", status)
	}
}

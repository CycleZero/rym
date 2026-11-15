// Package architecture 用 AST 测试把分层规则变成 CI 门禁。
//
// 这些测试只读源码（不编译、不连外部服务），因此可以放心跑在每次 PR 上。
package architecture_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/CycleZero/rym"

// repoRoot 从测试工作目录向上找到 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败：%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根目录（go.mod）")
		}
		dir = parent
	}
}

// walkGoFiles 遍历目录下的非测试 Go 文件。
func walkGoFiles(t *testing.T, root string, fn func(path, rel string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fn(path, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败：%v", root, err)
	}
}

// importsOf 解析文件的 import 列表。
func importsOf(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	out := make([]string, 0, len(file.Imports))
	for _, imp := range file.Imports {
		v, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// TestBizDoesNotImportProtoOrData biz 层必须与传输/持久化细节解耦。
func TestBizDoesNotImportProtoOrData(t *testing.T) {
	root := repoRoot(t)
	appDir := filepath.Join(root, "app")
	var violations []string

	walkGoFiles(t, appDir, func(path, rel string) {
		normalized := filepath.ToSlash(rel)
		if !strings.Contains(normalized, "/internal/biz/") {
			return
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasPrefix(imp, modulePath+"/api/") || strings.HasSuffix(imp, "/internal/data") {
				violations = append(violations, fmt.Sprintf("%s imports %s", normalized, imp))
			}
		}
	})

	if len(violations) > 0 {
		t.Errorf("biz 层不允许依赖 proto 契约或 data 层：\n%s", strings.Join(violations, "\n"))
	}
}

// TestServiceDoesNotImportData service 层只做 proto ↔ biz 适配，不能碰仓储实现。
func TestServiceDoesNotImportData(t *testing.T) {
	root := repoRoot(t)
	var violations []string

	walkGoFiles(t, filepath.Join(root, "app"), func(path, rel string) {
		normalized := filepath.ToSlash(rel)
		if !strings.Contains(normalized, "/internal/service/") {
			return
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasSuffix(imp, "/internal/data") {
				violations = append(violations, fmt.Sprintf("%s imports %s", normalized, imp))
			}
		}
	})

	if len(violations) > 0 {
		t.Errorf("service 层不允许 import data：\n%s", strings.Join(violations, "\n"))
	}
}

// TestNoAutoMigrate 生产代码禁止 AutoMigrate（表结构走编号 SQL）。
func TestNoAutoMigrate(t *testing.T) {
	root := repoRoot(t)
	var violations []string

	for _, subtree := range []string{"app", "pkg", "cmd", "conf"} {
		dir := filepath.Join(root, subtree)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		walkGoFiles(t, dir, func(path, rel string) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("读取 %s 失败：%v", path, err)
			}
			if strings.Contains(string(src), "AutoMigrate") {
				violations = append(violations, filepath.ToSlash(filepath.Join(subtree, rel)))
			}
		})
	}

	if len(violations) > 0 {
		t.Errorf("生产代码禁止 AutoMigrate（改用 app/<svc>/migrations/*.sql）：\n%s", strings.Join(violations, "\n"))
	}
}

// TestModuleIsolation 服务之间零 import：只能经 api/ 契约或 pkg/ 基础设施协作。
//
// 这是"app/<svc> 整个目录可 mv 成独立微服务"的结构保证。
func TestModuleIsolation(t *testing.T) {
	root := repoRoot(t)
	appDir := filepath.Join(root, "app")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("读取 app/ 失败：%v", err)
	}

	var violations []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		service := entry.Name()
		walkGoFiles(t, filepath.Join(appDir, service), func(path, rel string) {
			for _, imp := range importsOf(t, path) {
				other, ok := otherService(imp)
				if !ok || other == service {
					continue
				}
				violations = append(violations, fmt.Sprintf("app/%s/%s imports %s（应改为经 api/ 契约调用）",
					service, filepath.ToSlash(rel), imp))
			}
		})
	}

	if len(violations) > 0 {
		t.Errorf("服务之间禁止直接 import：\n%s", strings.Join(violations, "\n"))
	}
}

// otherService 判断 import 是否指向另一个服务。
//
// 判定标准是 app/ 后的第一个路径段，**不区分是否落在 internal/ 下**：
// 服务的公开装配面（provider.go / wire.go / conf/）同样不能被别的服务引用，
// 否则 `mv app/<svc>` 出去就编译不过——"可拆分"的保证正是在这里断掉的。
//
// 组合根（cmd/）不在遍历范围内，因此它引用 app/<svc> 不受本规则限制。
func otherService(imp string) (string, bool) {
	const prefix = modulePath + "/app/"
	rest, ok := strings.CutPrefix(imp, prefix)
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	return parts[0], true
}

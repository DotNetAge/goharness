// Glob 工具模式匹配语义的单元测试。
// 覆盖：globstar（**）分层匹配、裸 basename 任意深度匹配、目录作用域不被静默丢弃、
// 默认跳过重目录、head_limit 截断、修改时间降序排序。
// 直接调用 performGlob（不依赖沙箱注入，沙箱行为由 sandbox_integration_test.go 覆盖）。
package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeGlobTestTree 构造固定结构的临时目录树，返回根目录。
//
//	root/
//	  a.go
//	  src/b.go  src/sub/c.go  src/note.txt
//	  config/app.yaml  config/deep/d.yaml
//	  node_modules/e.go
//
// 修改时间按写入顺序递增（间隔 1 秒），保证排序断言确定性。
func writeGlobTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"a.go",
		"src/b.go", "src/sub/c.go", "src/note.txt",
		"config/app.yaml", "config/deep/d.yaml",
		"node_modules/e.go",
	}
	for i, rel := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(rel), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		mtime := time.Unix(1700000000+int64(i), 0)
		if err := os.Chtimes(abs, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
	}
	return root
}

// globFiles 调用 performGlob 并返回匹配的文件相对路径列表（'/' 分隔，mtime 降序）。
func globFiles(t *testing.T, root, pattern string, headLimit int) []string {
	t.Helper()
	res, err := performGlob(root, globParams{pattern: pattern, headLimit: headLimit}, 200)
	if err != nil {
		t.Fatalf("performGlob(%q): %v", pattern, err)
	}
	mm, ok := res.(MetaMap)
	if !ok {
		t.Fatalf("performGlob 返回类型异常: %T", res)
	}
	raw, _ := mm.Data["files"].([]string)
	var rels []string
	for _, f := range raw {
		if rel, err := filepath.Rel(root, f); err == nil {
			rels = append(rels, filepath.ToSlash(rel))
		}
	}
	return rels
}

func assertFiles(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("匹配数量不符：\n期望 %v\n实际 %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("匹配结果不符（位置 %d）：\n期望 %v\n实际 %v", i, want, got)
		}
	}
}

// TestGlobPatternSemantics 验证各模式形态的匹配语义。
func TestGlobPatternSemantics(t *testing.T) {
	root := writeGlobTestTree(t)

	t.Run("裸模式任意深度匹配且跳过 node_modules", func(t *testing.T) {
		got := globFiles(t, root, "*.go", 0)
		assertFiles(t, got, []string{"src/sub/c.go", "src/b.go", "a.go"})
	})

	t.Run("globstar 匹配零或多层目录", func(t *testing.T) {
		got := globFiles(t, root, "src/**/*.go", 0)
		assertFiles(t, got, []string{"src/sub/c.go", "src/b.go"})
	})

	t.Run("目录前缀作用域不被丢弃", func(t *testing.T) {
		// 回归用例：修复前 normalizeGlobPattern 会把 'src/**/*.go' 削成 '*.go'，
		// 导致作用域被静默放大到整棵树
		got := globFiles(t, root, "src/*.go", 0)
		assertFiles(t, got, []string{"src/b.go"})
	})

	t.Run("单层目录模式不递归", func(t *testing.T) {
		got := globFiles(t, root, "config/*.yaml", 0)
		assertFiles(t, got, []string{"config/app.yaml"})
	})

	t.Run("尾部 globstar 匹配子树全部文件", func(t *testing.T) {
		got := globFiles(t, root, "src/**", 0)
		assertFiles(t, got, []string{"src/note.txt", "src/sub/c.go", "src/b.go"})
	})

	t.Run("独立 globstar 匹配全部文件", func(t *testing.T) {
		got := globFiles(t, root, "**", 0)
		assertFiles(t, got, []string{
			"config/deep/d.yaml", "config/app.yaml", "src/note.txt",
			"src/sub/c.go", "src/b.go", "a.go",
		})
	})

	t.Run("反斜杠路径模式经归一化后保留目录作用域", func(t *testing.T) {
		// 回归用例：compileGlobPattern 把 '\' 归一化为 '/'，分支判定必须基于编译结果
		//（len(pat) > 1）而非原始串是否含 '/'，否则作用域被静默丢弃
		got := globFiles(t, root, `src\*.go`, 0)
		assertFiles(t, got, []string{"src/b.go"})
	})

	t.Run("尾随斜杠模式等价 basename 匹配", func(t *testing.T) {
		// 'config/' 编译后为单段 ['config']，按 basename 语义匹配名为 config 的文件；
		// 测试树中无该文件，应返回空集（锁定边界语义，防止静默变成任意深度匹配）
		got := globFiles(t, root, "config/", 0)
		assertFiles(t, got, nil)
	})
}

// TestGlobHeadLimit 验证 head_limit 按 mtime 降序截断。
func TestGlobHeadLimit(t *testing.T) {
	root := writeGlobTestTree(t)
	got := globFiles(t, root, "**", 3)
	assertFiles(t, got, []string{"config/deep/d.yaml", "config/app.yaml", "src/note.txt"})
}

// TestGlobSortByModTime 验证修改时间降序（最新优先）。
func TestGlobSortByModTime(t *testing.T) {
	root := writeGlobTestTree(t)
	// 把 a.go 改为最新文件
	mtime := time.Unix(1700000100, 0)
	if err := os.Chtimes(filepath.Join(root, "a.go"), mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	got := globFiles(t, root, "*.go", 0)
	if got[0] != "a.go" {
		t.Fatalf("最新修改的文件应排首位，实际：%v", got)
	}
}

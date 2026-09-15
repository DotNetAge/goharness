// LS 工具核心逻辑的单元测试（不经沙箱，直接调用 performLS）。
// 覆盖：递归模式跳过 node_modules 等重目录的子项展开、目录条目元数据完整性。
package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLS_RecursiveSkipsHeavyDirs 验证 recursive 模式列出重目录本身但不展开其子项，
// 避免依赖目录淹没 500 条上限与上下文。
func TestLS_RecursiveSkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"src/b.go",
		"node_modules/pkg/a.js",
		"dist/out.js",
	}
	for _, rel := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(rel), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	res, err := performLS(root, lsParams{path: root, recursive: true})
	if err != nil {
		t.Fatalf("performLS: %v", err)
	}
	items, _ := res["items"].([]map[string]any)

	byName := make(map[string]map[string]any)
	for _, item := range items {
		name, _ := item["name"].(string)
		byName[name] = item
	}

	// 普通目录正常展开子项
	src, ok := byName["src"]
	if !ok {
		t.Fatalf("src 目录应被列出，实际条目：%v", byName)
	}
	if children, _ := src["children"].([]map[string]any); len(children) != 1 {
		t.Errorf("src 应展开 1 个子项，实际 %v", src["children"])
	}

	// 重目录仅列出本身，不展开子项，且带 children_skipped 标记（防下游误读为空目录）
	for _, heavy := range []string{"node_modules", "dist"} {
		item, ok := byName[heavy]
		if !ok {
			t.Errorf("重目录 %s 本身应被列出（保留结构可见性）", heavy)
			continue
		}
		if _, has := item["children"]; has {
			t.Errorf("重目录 %s 不应展开 children", heavy)
		}
		if skipped, _ := item["children_skipped"].(bool); !skipped {
			t.Errorf("重目录 %s 应带 children_skipped 标记", heavy)
		}
	}

	// 普通目录不应带 children_skipped 标记
	if skipped, has := src["children_skipped"]; has {
		t.Errorf("普通目录 src 不应带 children_skipped 标记，实际：%v", skipped)
	}
}

package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGlobAbortsOnCancelledContext 验证 Glob 的全树遍历在 ctx 已取消时立刻放弃，
// 并报「已停止」而不是拿半截结果当完整匹配返回。
//
// 背景：performGlob 与 Grep 的内置遍历同构（filepath.WalkDir 扫全树），此前既
// 不查 ctx 也没有超时——大仓库里 `**/*` 会让整轮执行停不下来（用户点停止无效）。
func TestGlobAbortsOnCancelledContext(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		path := filepath.Join(root, "sub", "f"+string(rune('a'+i))+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("准备目录失败: %v", err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("准备测试文件失败: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	<-ctx.Done()

	start := time.Now()
	_, err := performGlob(ctx, root, globParams{pattern: "**/*.txt"}, 200)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
	}
	if err == nil {
		t.Fatal("取消时应返回错误说明，不能把中止当成完整匹配结果")
	}
}

// TestGlobCompletesNormally 对照组：ctx 正常时照常返回匹配，确认中止分支没有伤害正常路径。
func TestGlobCompletesNormally(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hit.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("准备测试文件失败: %v", err)
	}

	res, err := performGlob(context.Background(), root, globParams{pattern: "*.md"}, 200)
	if err != nil {
		t.Fatalf("正常搜索不应报错: %v", err)
	}
	mm, ok := res.(MetaMap)
	if !ok {
		t.Fatalf("结果类型异常: %T", res)
	}
	files, _ := mm.Data["files"].([]string)
	if len(files) != 1 {
		t.Fatalf("应命中 1 个文件，实际 %v", files)
	}
}

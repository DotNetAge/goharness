package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGrepNativeAbortsOnCancelledContext 验证内置遍历路径（未装 ripgrep 时启用）
// 在 ctx 已取消时立刻放弃扫描并报「已停止」，而不是继续把目录走完、也不把
// 中止当成「未找到匹配」返回——后者会让模型以为搜完了。
//
// 背景：该路径此前既不检查 ctx 也没有超时，大仓库里一次宽泛搜索会空转数分钟，
// 用户点停止也停不下来（工具不返回则整轮执行无法退出）。
func TestGrepNativeAbortsOnCancelledContext(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
			t.Fatalf("准备测试文件失败: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	<-ctx.Done()

	start := time.Now()
	_, err := (&GrepTool{MaxResults: 100, MaxOutputChars: 50000}).runNative(ctx, grepParams{pattern: "alpha"}, dir)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
	}
	if err == nil {
		t.Fatal("取消时应返回错误说明，不能把中止当成「未找到匹配」")
	}
}

// TestGrepNativeCompletesNormally 对照组：ctx 正常时照常搜索出结果，
// 确认上面的中止分支没有伤害正常路径。
func TestGrepNativeCompletesNormally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hit.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("准备测试文件失败: %v", err)
	}

	got, err := (&GrepTool{MaxResults: 100, MaxOutputChars: 50000}).runNative(
		context.Background(), grepParams{pattern: "alpha"}, dir,
	)
	if err != nil {
		t.Fatalf("正常搜索不应报错: %v", err)
	}
	value, ok := got.(MetaString)
	if !ok {
		t.Fatalf("结果类型异常: %T", got)
	}
	if value.Value == "" || value.Value == "未找到匹配项。" {
		t.Fatalf("应命中 alpha，实际结果: %q", value.Value)
	}
}

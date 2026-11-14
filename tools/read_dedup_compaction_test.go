package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
)

// 回归：压缩把 cursor 前移后，Read 去重缓存必须失效。
// 窗口已空时若仍返回「未变化，引用此前结果」，模型会引用不在上下文里的内容。
func TestRead_DedupCacheInvalidatedByCursor(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store := newMockSessionStore()
	sb, err := sandbox.NewSandbox(&sandbox.SandboxPolicy{AllowedDirs: []string{dir}}, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	s1, err := session.New("agent", "", dir, store, logging.NewNopLogger(), session.WithSandbox(sb))
	if err != nil {
		t.Fatal(err)
	}
	read := NewReadTool()
	ctx1 := WithToolContext(context.Background(), &ToolContext{Session: s1, Logger: logging.NewNopLogger()})
	if _, err := read.Execute(ctx1, map[string]any{"filePath": file}); err != nil {
		t.Fatal(err)
	}

	// 模拟压缩落盘效果：cursor 移到末尾 + 重新 Load，活跃窗口为空。
	_ = store.SetCursor(context.Background(), s1.ID(), 1)
	s2, err := session.Load(context.Background(), s1.ID(), "agent", store, logging.NewNopLogger(), session.WithSandbox(sb))
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Current()) != 0 {
		t.Fatalf("模拟窗口未清空: %d", len(s2.Current()))
	}
	ctx2 := WithToolContext(context.Background(), &ToolContext{Session: s2, Logger: logging.NewNopLogger()})
	res, err := read.Execute(ctx2, map[string]any{"filePath": file})
	if err != nil {
		t.Fatal(err)
	}
	content := res.(*ReadResult).Data.Content
	if strings.Contains(content, "未发生变化") {
		t.Fatalf("窗口已空但命中旧缓存: %q", content)
	}
	if !strings.Contains(content, "hello world") {
		t.Fatalf("应返回全文: %q", content)
	}
}

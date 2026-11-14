//go:build knownissue

// 已知缺陷期望行为测试。
//
// 默认 go test ./... 不编译本文件；运行 `go test -tags=knownissue ./tools` 会
// 以失败形式列出当前未修复的 B 类缺陷。每修复一项，把对应用例从本文件移出，
// 去掉 knownissue 标签，转为正常回归。
package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
)

func knownIssueSession(t *testing.T, id, dir string) *session.Session {
	t.Helper()
	sb, err := sandbox.NewSandbox(&sandbox.SandboxPolicy{AllowedDirs: []string{dir}}, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(id, "", dir, newMockSessionStore(), logging.NewNopLogger(), session.WithSandbox(sb))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// V-05：压缩（cursor 前移）后，Read 去重缓存必须失效，返回全文而不是「未变化」。
func TestKnownIssue_ReadDedupAfterCompaction(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(file, []byte("hello world\n"), 0644)

	store := newMockSessionStore()
	sb, _ := sandbox.NewSandbox(&sandbox.SandboxPolicy{AllowedDirs: []string{dir}}, logging.NewNopLogger())
	s1, _ := session.New("agent", "", dir, store, logging.NewNopLogger(), session.WithSandbox(sb))
	ctx1 := WithToolContext(context.Background(), &ToolContext{Session: s1, Logger: logging.NewNopLogger()})
	read := NewReadTool()
	if _, err := read.Execute(ctx1, map[string]any{"filePath": file}); err != nil {
		t.Fatal(err)
	}

	// 模拟压缩落盘：cursor 移到末尾 + 重新 Load，窗口为空。
	_ = store.SetCursor(context.Background(), s1.ID(), 1)
	s2, err := session.Load(context.Background(), s1.ID(), "agent", store, logging.NewNopLogger(), session.WithSandbox(sb))
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Current()) != 0 {
		t.Fatalf("模拟窗口未清空: %d", len(s2.Current()))
	}
	ctx2 := WithToolContext(context.Background(), &ToolContext{Session: s2, Logger: logging.NewNopLogger()})
	r2, err := read.Execute(ctx2, map[string]any{"filePath": file})
	if err != nil {
		t.Fatal(err)
	}
	content := r2.(*ReadResult).Data.Content
	if strings.Contains(content, "未发生变化") {
		t.Fatalf("窗口已空但命中旧缓存：%q", content)
	}
}

// V-06：读前写约束必须按会话隔离，B 会话未读不应能直接覆盖 A 读过的文件。
func TestKnownIssue_ReadBeforeWriteIsSessionScoped(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "b.txt")
	_ = os.WriteFile(file, []byte("origin\n"), 0644)

	sA := knownIssueSession(t, "sess-A", dir)
	ctxA := WithToolContext(context.Background(), &ToolContext{Session: sA, Logger: logging.NewNopLogger()})
	if _, err := NewReadTool().Execute(ctxA, map[string]any{"filePath": file}); err != nil {
		t.Fatal(err)
	}

	sB := knownIssueSession(t, "sess-B", dir)
	ctxB := WithToolContext(context.Background(), &ToolContext{Session: sB, Logger: logging.NewNopLogger()})
	_, err := NewWriteTool().Execute(ctxB, map[string]any{"filePath": file, "content": "overwritten\n"})
	if err == nil {
		t.Fatal("B 会话未读却成功写入：读前写约束跨会话失效")
	}
}

// V-15：Grep 原生路径必须能扫描超过 64KB 的单行，不得静默报「未找到」。
func TestKnownIssue_GrepNativeLongLine(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "long.txt")
	_ = os.WriteFile(file, []byte(strings.Repeat("a", 100*1024)+"NEEDLE\n"), 0644)

	re := regexp.MustCompile("NEEDLE")
	_, count, err := grepScanFile(file, "long.txt", re, "content")
	if err != nil {
		t.Fatalf("单行超过 64KB 时扫描报错: %v", err)
	}
	if count == 0 {
		t.Fatalf("100KB 单行文件中的 NEEDLE 被静默漏报")
	}
}

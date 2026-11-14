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

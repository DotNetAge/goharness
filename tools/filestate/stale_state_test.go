package filestate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 观测回归：CheckStale 应报告跨会话归属，但当前行为仍放行（尚未按会话隔离）。
func TestCheckStale_ReportsCrossSessionOwner(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	content := []byte("hello\n")
	if err := os.WriteFile(file, content, 0644); err != nil {
		t.Fatal(err)
	}
	SetStale("session-A", file, time.Now(), content)
	defer DeleteStale(file)

	if err, owner := CheckStale("session-B", file); err != nil || owner != "session-A" {
		t.Fatalf("跨会话未报告归属: err=%v owner=%q", err, owner)
	}
	if err, owner := CheckStale("session-A", file); err != nil || owner != "" {
		t.Fatalf("同会话不应报告跨会话: err=%v owner=%q", err, owner)
	}
}

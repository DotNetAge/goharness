package filestate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupStaleFile(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	content := []byte("hello\n")
	if err := os.WriteFile(file, content, 0644); err != nil {
		t.Fatalf("准备文件: %v", err)
	}
	return file, content
}

// V-06 核心断言：读前写约束按会话隔离。
// A 会话读过，不能作为 B 会话放行的依据——B 从未看过文件内容，
// 放行写入等于基于陌生内容覆盖。
func TestCheckStale_CrossSessionIsNotRead(t *testing.T) {
	file, content := setupStaleFile(t)
	SetStale("session-A", file, time.Now(), content)
	defer DeleteStale("session-A", file)

	err, owner := CheckStale("session-B", file)
	if err == nil {
		t.Fatal("B 会话未读过该文件，应返回 ErrNotRead 而非放行")
	}
	if owner != "session-A" {
		t.Errorf("应报告其他会话归属 session-A，实际 %q", owner)
	}
	// 错误文案要能让调用方理解「别人读过≠我读过」
	if !strings.Contains(err.Error(), "当前会话") {
		t.Errorf("错误文案应点明当前会话未读，实际: %v", err)
	}
}

// 同会话读过则放行，且不报告跨会话归属。
func TestCheckStale_SameSessionAllowed(t *testing.T) {
	file, content := setupStaleFile(t)
	SetStale("session-A", file, time.Now(), content)
	defer DeleteStale("session-A", file)

	if err, owner := CheckStale("session-A", file); err != nil || owner != "" {
		t.Fatalf("同会话应放行且无跨会话归属: err=%v owner=%q", err, owner)
	}
}

// 两个会话各自持有独立记录：谁读过谁才能写，互不覆盖。
func TestSetStale_PerSessionRecordsCoexist(t *testing.T) {
	file, content := setupStaleFile(t)
	SetStale("session-A", file, time.Now(), content)
	SetStale("session-B", file, time.Now(), content)
	defer func() {
		DeleteStale("session-A", file)
		DeleteStale("session-B", file)
	}()

	if err, _ := CheckStale("session-A", file); err != nil {
		t.Errorf("A 的记录不应被 B 覆盖: %v", err)
	}
	if err, _ := CheckStale("session-B", file); err != nil {
		t.Errorf("B 读过后应放行: %v", err)
	}
}

// DeleteStale 只清调用方自己的记录，不影响其他会话。
func TestDeleteStale_ScopedToSession(t *testing.T) {
	file, content := setupStaleFile(t)
	SetStale("session-A", file, time.Now(), content)
	SetStale("session-B", file, time.Now(), content)

	DeleteStale("session-A", file)

	if err, _ := CheckStale("session-A", file); err == nil {
		t.Error("A 的记录应已被删除")
	}
	if err, _ := CheckStale("session-B", file); err != nil {
		t.Errorf("删 A 的记录不应影响 B: %v", err)
	}

	DeleteStale("session-B", file)
}

// 内容被外部改动后仍应报 stale（不受会话隔离改动影响）。
func TestCheckStale_DetectsExternalModification(t *testing.T) {
	file, content := setupStaleFile(t)
	SetStale("session-A", file, time.Now(), content)
	defer DeleteStale("session-A", file)

	if err := os.WriteFile(file, []byte("changed by someone else\n"), 0644); err != nil {
		t.Fatalf("外部修改: %v", err)
	}
	// 确保 mtime 真的变化（部分文件系统精度较粗）
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(file, future, future); err != nil {
		t.Fatalf("调整 mtime: %v", err)
	}

	if err, _ := CheckStale("session-A", file); err == nil {
		t.Fatal("文件已外部修改，应返回 stale 错误")
	}
}

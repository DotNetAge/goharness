package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSession_PendingPermission_PersistsAcrossLoad 验证待处理授权跨 Load 存活：
// daemon 每个 user.message 都会 Load 全新 Session 实例，若 pending 只在内存保存，
// 第二轮魔法词到达时必然丢失，主会话授权链断裂。本测试模拟「设置 pending →
// 重建 Session（等价于重新 Load）→ 恢复」的完整闭环。
func TestSession_PendingPermission_PersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	s1 := newTestSession("sess-1", "agent", store)
	s1.SetPendingPermission(PendingPermission{
		ToolName:      "Bash",
		ToolCallID:    "tc1",
		Arguments:     map[string]any{"command": "ls /"},
		Reason:        "越权访问",
		SecurityLevel: "high_risk",
	})

	// 验证文件已落盘
	if _, err := os.Stat(filepath.Join(dir, pendingPermissionFileName)); err != nil {
		t.Fatalf("待处理授权文件未写入: %v", err)
	}

	// 重建 Session（等价于 daemon 下一轮 user.message 的 Load）
	s2 := newTestSession("sess-1", "agent", store)
	if !s2.HasPendingPermission() {
		t.Fatal("重建会话后未恢复待处理授权")
	}
	p := s2.TakePendingPermission()
	if p == nil {
		t.Fatal("TakePendingPermission 返回 nil")
	}
	if p.ToolName != "Bash" || p.ToolCallID != "tc1" {
		t.Fatalf("恢复的 pending 不完整: %+v", p)
	}
	if p.Arguments["command"] != "ls /" {
		t.Fatalf("恢复的 Arguments 不完整: %+v", p.Arguments)
	}
	if p.SecurityLevel != "high_risk" {
		t.Fatalf("恢复的 SecurityLevel 不完整: %q", p.SecurityLevel)
	}

	// Take 后文件应被删除，再重建不应残留
	if _, err := os.Stat(filepath.Join(dir, pendingPermissionFileName)); !os.IsNotExist(err) {
		t.Fatalf("Take 后待处理授权文件未删除: %v", err)
	}
	s3 := newTestSession("sess-1", "agent", store)
	if s3.HasPendingPermission() {
		t.Fatal("Take 后重建会话仍残留待处理授权")
	}
}

// TestSession_PendingPermission_NoSessionDir 验证无存储目录的会话（纯内存测试环境）
// 仅内存保存、不 panic，行为与旧实现一致。
func TestSession_PendingPermission_NoSessionDir(t *testing.T) {
	s := newTestSession("sess-2", "agent", newMockStore())
	s.SetPendingPermission(PendingPermission{
		ToolName:   "Write",
		ToolCallID: "tc2",
		Arguments:  map[string]any{"filePath": "/etc/passwd"},
	})
	if !s.HasPendingPermission() {
		t.Fatal("纯内存会话应保留待处理授权")
	}
	p := s.TakePendingPermission()
	if p == nil || p.ToolName != "Write" {
		t.Fatalf("TakePendingPermission 结果异常: %+v", p)
	}
}

// TestSession_PendingPermission_LoadAfterTimeout 兜底验证：整个测试在超时内完成，
// 避免死锁（pendingMu / loadingMu 的加锁顺序）。
func TestSession_PendingPermission_LoadAfterTimeout(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		dir := t.TempDir()
		store := newMockStore()
		store.sessionDir = dir
		s := newTestSession("sess-3", "agent", store)
		_ = s.Current() // 触发 ensureLoaded
		s.SetPendingPermission(PendingPermission{ToolName: "Bash", ToolCallID: "tc3"})
		_ = s.TakePendingPermission()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("测试超时（疑似 pendingMu 死锁）")
	}
}

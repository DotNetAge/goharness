package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSession_PendingAskUser_PersistsAcrossLoad 验证挂起提问跨 Load 存活：
// 主会话以 ask_user_pending 终止后，daemon 每个 user.message 都会 Load 全新
// Session 实例，若提问只在内存保存，用户回答到达时必然丢失，AskUser 的
// tool 结果消息无法补全，会话遗留配对断裂。本测试模拟「登记提问 → 重建
// Session（等价于重新 Load）→ 恢复」的完整闭环。
func TestSession_PendingAskUser_PersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	s1 := newTestSession("sess-ask-1", "agent", store)
	s1.SetPendingAskUser(PendingAskUser{
		ToolCallID: "ask_tc1",
		Question:   "请确认部署目标环境",
	})

	// 验证文件已落盘
	if _, err := os.Stat(filepath.Join(dir, pendingAskFileName)); err != nil {
		t.Fatalf("挂起提问文件未写入: %v", err)
	}

	// 重建 Session（等价于 daemon 下一轮 user.message 的 Load）
	s2 := newTestSession("sess-ask-1", "agent", store)
	if !s2.HasPendingAskUser() {
		t.Fatal("重建会话后未恢复挂起提问")
	}
	p := s2.TakePendingAskUser()
	if p == nil {
		t.Fatal("TakePendingAskUser 返回 nil")
	}
	if p.ToolCallID != "ask_tc1" || p.Question != "请确认部署目标环境" {
		t.Fatalf("恢复的挂起提问不完整: %+v", p)
	}

	// Take 后文件应被删除，再重建不应残留
	if _, err := os.Stat(filepath.Join(dir, pendingAskFileName)); !os.IsNotExist(err) {
		t.Fatalf("Take 后挂起提问文件未删除: %v", err)
	}
	s3 := newTestSession("sess-ask-1", "agent", store)
	if s3.HasPendingAskUser() {
		t.Fatal("Take 后重建会话仍残留挂起提问")
	}
}

// TestSession_PendingAskUser_NoSessionDir 验证无存储目录的会话（纯内存测试环境）
// 仅内存保存、不 panic，行为与待处理授权一致。
func TestSession_PendingAskUser_NoSessionDir(t *testing.T) {
	s := newTestSession("sess-ask-2", "agent", newMockStore())
	s.SetPendingAskUser(PendingAskUser{ToolCallID: "ask_tc2", Question: "纯内存提问"})
	if !s.HasPendingAskUser() {
		t.Fatal("纯内存会话应保留挂起提问")
	}
	p := s.TakePendingAskUser()
	if p == nil || p.ToolCallID != "ask_tc2" {
		t.Fatalf("TakePendingAskUser 结果异常: %+v", p)
	}
}

// TestSession_PendingAskUser_LoadAfterTimeout 兜底验证：整个测试在超时内完成，
// 避免死锁（pendingMu / loadingMu 的加锁顺序）。
func TestSession_PendingAskUser_LoadAfterTimeout(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		dir := t.TempDir()
		store := newMockStore()
		store.sessionDir = dir
		s := newTestSession("sess-ask-3", "agent", store)
		_ = s.Current() // 触发 ensureLoaded
		s.SetPendingAskUser(PendingAskUser{ToolCallID: "ask_tc3"})
		_ = s.TakePendingAskUser()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("测试超时（疑似 pendingMu 死锁）")
	}
}

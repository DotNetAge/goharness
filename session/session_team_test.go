package session

import (
	"context"
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// 测试目标：TeamName 是会话级事实——New/WithTeam 注入、Load 从元数据恢复，
// 保证会话重载后 System Prompt 的团队段重建与原会话一致。

func newTestLogger() logging.Logger {
	return logging.NewNopLogger()
}

// WithTeam 在 New 路径注入团队名，TeamName() 访问器可读回。
func TestSessionWithTeamConfig(t *testing.T) {
	s, err := New("agent-a", "", t.TempDir(), newMockStore(), newTestLogger(), WithTeam("alpha"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if got := s.TeamName(); got != "alpha" {
		t.Fatalf("TeamName = %q, 期望 %q", got, "alpha")
	}
}

// 不绑定团队时 TeamName 为空（个人会话）。
func TestSessionWithoutTeamDefaultsEmpty(t *testing.T) {
	s, err := New("agent-a", "", t.TempDir(), newMockStore(), newTestLogger())
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if got := s.TeamName(); got != "" {
		t.Fatalf("TeamName = %q, 期望空（个人会话）", got)
	}
}

// Load 路径：团队绑定从会话元数据恢复（创建时经 WithTeamNameOption 写入 meta）。
func TestSessionLoadRestoresTeamName(t *testing.T) {
	store := newMockStore()
	store.getMetaResult = &SessionInfo{
		SessionID: "01TEAMTEST",
		AgentName: "leader-a",
		TeamName:  "alpha",
	}

	s, err := Load(context.Background(), "01TEAMTEST", "", store, newTestLogger())
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got := s.TeamName(); got != "alpha" {
		t.Fatalf("Load 恢复的 TeamName = %q, 期望 %q", got, "alpha")
	}
	if got := s.AgentName(); got != "leader-a" {
		t.Fatalf("AgentName = %q, 期望 leader-a", got)
	}
}

// 个人会话（meta 无 TeamName）Load 后仍为空。
func TestSessionLoadWithoutTeamStaysEmpty(t *testing.T) {
	store := newMockStore()
	store.getMetaResult = &SessionInfo{
		SessionID: "01PERSONAL",
		AgentName: "solo",
	}

	s, err := Load(context.Background(), "01PERSONAL", "", store, newTestLogger())
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got := s.TeamName(); got != "" {
		t.Fatalf("TeamName = %q, 期望空", got)
	}
}

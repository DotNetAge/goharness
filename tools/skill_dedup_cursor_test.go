package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
)

// 回归：技能去重缓存必须随压缩游标失效；同游标重复加载仍走短回执。
func TestSkillTool_DedupCacheInvalidatedByCursor(t *testing.T) {
	const name = "cursor-aware-skill"
	base := &baseRegistry{skills: map[string]*skill.Skill{
		name: {Name: name, Instructions: "技能完整指令", RootDir: "/tmp/skills/" + name},
	}}
	tool := NewSkillTool(base.GetSkill, nil)

	store := newMockSessionStore()
	s1, err := session.New("agent", "", "/tmp/proj", store, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx1 := WithToolContext(context.Background(), &ToolContext{Session: s1, Logger: logging.NewNopLogger()})
	first, err := tool.Execute(ctx1, map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.(map[string]any)["content"].(string), "技能完整指令") {
		t.Fatalf("首次加载未返回完整指令: %v", first)
	}

	// 同游标重复加载：应命中缓存，返回短回执。
	second, err := tool.Execute(ctx1, map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.(map[string]any)["content"].(string), "已加载") {
		t.Fatalf("同游标重复加载未命中缓存: %v", second)
	}

	// 模拟压缩：cursor 前移 + 重新 Load，旧指令已不在窗口。
	_ = store.SetCursor(context.Background(), s1.ID(), 1)
	s2, err := session.Load(context.Background(), s1.ID(), "agent", store, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx2 := WithToolContext(context.Background(), &ToolContext{Session: s2, Logger: logging.NewNopLogger()})
	third, err := tool.Execute(ctx2, map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	content := third.(map[string]any)["content"].(string)
	if strings.Contains(content, "已加载") {
		t.Fatalf("压缩后仍返回已加载回执: %v", content)
	}
	if !strings.Contains(content, "技能完整指令") {
		t.Fatalf("压缩后应重新加载完整指令: %v", content)
	}
}

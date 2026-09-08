package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
)

// overlayRegistry 是测试用会话级技能覆盖注册表。
type overlayRegistry struct {
	skills map[string]*skill.Skill
}

func (r *overlayRegistry) GetSkill(name string) (*skill.Skill, error) {
	sk, ok := r.skills[name]
	if !ok {
		return nil, skill.ErrSkillNotFound
	}
	return sk, nil
}

// baseRegistry 是测试用 Runtime 基础注册表。
type baseRegistry struct {
	skills map[string]*skill.Skill
}

func (r *baseRegistry) GetSkill(name string) (*skill.Skill, error) {
	sk, ok := r.skills[name]
	if !ok {
		return nil, skill.ErrSkillNotFound
	}
	return sk, nil
}

func TestSkillTool_OverlayPriority(t *testing.T) {
	// 技能去重缓存是进程级全局（同名技能只完整加载一次），
	// 因此各子测试使用互不相同的技能名，避免去重提示干扰断言。
	base := &baseRegistry{skills: map[string]*skill.Skill{
		"base-only-skill":  {Name: "base-only-skill", Instructions: "基础独有技能", RootDir: "/base/only"},
		"shadowed-skill":   {Name: "shadowed-skill", Instructions: "基础版本", RootDir: "/base/shadowed"},
	}}
	overlay := &overlayRegistry{skills: map[string]*skill.Skill{
		"shadowed-skill":   {Name: "shadowed-skill", Instructions: "项目覆盖版本", RootDir: "/proj/shadowed"},
		"project-only-skill": {Name: "project-only-skill", Instructions: "项目独有技能", RootDir: "/proj/only"},
	}}

	tool := NewSkillTool(base.GetSkill)

	t.Run("会话覆盖同名技能优先于基础注册表", func(t *testing.T) {
		sess := &session.Session{}
		sess.SetSkillOverlay(overlay)
		ctx := WithToolContext(context.Background(), &ToolContext{Session: sess})

		result, err := tool.Execute(ctx, map[string]any{"name": "shadowed-skill"})
		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		m := result.(map[string]any)
		if m["content"] != "项目覆盖版本" {
			t.Fatalf("同名技能应取会话覆盖版本, 实际: %v", m["content"])
		}
	})

	t.Run("覆盖未命中的技能回退基础注册表", func(t *testing.T) {
		sess := &session.Session{}
		sess.SetSkillOverlay(overlay)
		ctx := WithToolContext(context.Background(), &ToolContext{Session: sess})

		result, err := tool.Execute(ctx, map[string]any{"name": "base-only-skill"})
		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		m := result.(map[string]any)
		if m["content"] != "基础独有技能" {
			t.Fatalf("应回退基础注册表, 实际: %v", m["content"])
		}
	})

	t.Run("项目独有技能仅经会话覆盖可检索", func(t *testing.T) {
		sess := &session.Session{}
		sess.SetSkillOverlay(overlay)
		ctx := WithToolContext(context.Background(), &ToolContext{Session: sess})

		result, err := tool.Execute(ctx, map[string]any{"name": "project-only-skill"})
		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		m := result.(map[string]any)
		if !strings.Contains(m["content"].(string), "项目独有") {
			t.Fatalf("应返回项目技能内容, 实际: %v", m["content"])
		}
	})

	t.Run("未挂载覆盖时项目技能不可检索", func(t *testing.T) {
		sess := &session.Session{}
		ctx := WithToolContext(context.Background(), &ToolContext{Session: sess})

		if _, err := tool.Execute(ctx, map[string]any{"name": "never-loaded-skill"}); err == nil {
			t.Fatalf("未挂载覆盖时应返回未找到错误")
		}
	})
}

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
)

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

// nopStore / nopLogger 是测试用空实现（嵌入接口，Execute 仅读取会话
// ProjectDir，不触发存储与日志调用）。
type nopStore struct{ session.SessionStore }
type nopLogger struct{ logging.Logger }

// newSessionWithProjectDir 构造一个绑定指定项目目录的会话。
func newSessionWithProjectDir(t *testing.T, projectDir string) *session.Session {
	t.Helper()
	sess, err := session.New("test-agent", "", projectDir, nopStore{}, nopLogger{})
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	return sess
}

func TestSkillTool_ProjectFallback(t *testing.T) {
	// 技能去重缓存是进程级全局（同名技能只完整加载一次），
	// 因此各子测试使用互不相同的技能名，避免去重提示干扰断言。
	base := &baseRegistry{skills: map[string]*skill.Skill{
		"base-only-skill": {Name: "base-only-skill", Instructions: "基础独有技能", RootDir: "/base/only"},
	}}

	var resolverCalls []string
	resolver := func(projectDir, name string) (*skill.Skill, error) {
		resolverCalls = append(resolverCalls, projectDir+"|"+name)
		if name == "proj-skill" {
			return &skill.Skill{Name: name, Instructions: "动态技能指令", RootDir: projectDir + "/.agents/skills/" + name}, nil
		}
		return nil, skill.ErrSkillNotFound
	}

	tool := NewSkillTool(base.GetSkill, resolver)

	t.Run("项目级命中时优先于基础注册表", func(t *testing.T) {
		ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-a")})

		result, err := tool.Execute(ctx, map[string]any{"name": "proj-skill"})
		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		m := result.(map[string]any)
		if !strings.Contains(m["content"].(string), "动态技能") {
			t.Fatalf("应返回动态技能内容, 实际: %v", m["content"])
		}
		// 项目级优先：resolver 命中后不应再查基础注册表
		if len(resolverCalls) != 1 || resolverCalls[0] != "/tmp/proj-a|proj-skill" {
			t.Fatalf("解析器调用记录不符: %v", resolverCalls)
		}
	})

	t.Run("项目级与基础库均未命中返回未找到错误", func(t *testing.T) {
		ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-b")})

		if _, err := tool.Execute(ctx, map[string]any{"name": "never-exists-skill"}); err == nil {
			t.Fatalf("项目级与基础库均未命中时应返回未找到错误")
		}
	})

	t.Run("未注入解析器时动态技能不可检索", func(t *testing.T) {
		bare := NewSkillTool(base.GetSkill, nil)
		ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-c")})

		if _, err := bare.Execute(ctx, map[string]any{"name": "other-proj-skill"}); err == nil {
			t.Fatalf("未注入解析器时应返回未找到错误")
		}
	})
}

func TestSkillTool_ProjectPriorityOverBase(t *testing.T) {
	// 同名技能同时存在于基础注册表与项目级时，
	// 应返回项目级进化副本的内容（位置即覆盖）。
	base := &baseRegistry{skills: map[string]*skill.Skill{
		"dual-skill": {Name: "dual-skill", Instructions: "基础版指令", RootDir: "/base/dual"},
	}}
	resolver := func(projectDir, name string) (*skill.Skill, error) {
		if name == "dual-skill" {
			return &skill.Skill{Name: name, Instructions: "项目级进化副本指令", RootDir: projectDir + "/.agents/skills/" + name}, nil
		}
		return nil, skill.ErrSkillNotFound
	}

	tool := NewSkillTool(base.GetSkill, resolver)
	ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-prio")})

	result, err := tool.Execute(ctx, map[string]any{"name": "dual-skill"})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	m := result.(map[string]any)
	if !strings.Contains(m["content"].(string), "进化副本") {
		t.Fatalf("同名技能应返回项目级副本内容, 实际: %v", m["content"])
	}
	if m["root_dir"].(string) != "/tmp/proj-prio/.agents/skills/dual-skill" {
		t.Fatalf("root_dir 应指向项目级副本, 实际: %v", m["root_dir"])
	}
}

func TestSkillTool_DedupReloadsEvolvedCopy(t *testing.T) {
	// 进化感知判重：同名技能的进化副本内容更新后，
	// Skill 工具应重新完整加载而非返回"已加载"提示。
	name := "evolvable-skill"
	projectDir := "/tmp/proj-evo"
	base := &baseRegistry{skills: map[string]*skill.Skill{}}
	current := &skill.Skill{Name: name, Instructions: "第一版指令", RootDir: projectDir + "/.agents/skills/" + name}
	resolver := func(dir, n string) (*skill.Skill, error) {
		if n == name {
			return current, nil
		}
		return nil, skill.ErrSkillNotFound
	}

	tool := NewSkillTool(base.GetSkill, resolver)
	ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, projectDir)})

	// 首次调用：完整加载第一版
	result, err := tool.Execute(ctx, map[string]any{"name": name})
	if err != nil {
		t.Fatalf("首次 Execute 失败: %v", err)
	}
	if m := result.(map[string]any); !strings.Contains(m["content"].(string), "第一版") {
		t.Fatalf("首次调用应返回第一版完整内容, 实际: %v", m["content"])
	}

	// 内容未变时再次调用：返回"已加载"提示
	result, err = tool.Execute(ctx, map[string]any{"name": name})
	if err != nil {
		t.Fatalf("重复 Execute 失败: %v", err)
	}
	if m := result.(map[string]any); !strings.Contains(m["content"].(string), "已加载") {
		t.Fatalf("内容未变时应返回已加载提示, 实际: %v", m["content"])
	}

	// 模拟进化：副本内容更新后再次调用，应返回新版完整内容
	current = &skill.Skill{Name: name, Instructions: "第二版进化指令", RootDir: projectDir + "/.agents/skills/" + name}
	result, err = tool.Execute(ctx, map[string]any{"name": name})
	if err != nil {
		t.Fatalf("进化后 Execute 失败: %v", err)
	}
	if m := result.(map[string]any); !strings.Contains(m["content"].(string), "第二版进化") {
		t.Fatalf("进化副本更新后应重新完整加载, 实际: %v", m["content"])
	}
}

func TestSkillTool_NoProjectDirFallsToBase(t *testing.T) {
	// 会话未绑定项目目录时应直接走基础注册表（回归保护）。
	base := &baseRegistry{skills: map[string]*skill.Skill{
		"bare-base-skill": {Name: "bare-base-skill", Instructions: "基础库内容", RootDir: "/base/bare"},
	}}

	tool := NewSkillTool(base.GetSkill, func(projectDir, name string) (*skill.Skill, error) {
		t.Fatalf("无会话上下文时不应调用项目级解析器")
		return nil, skill.ErrSkillNotFound
	})
	// ToolContext 不含 Session（无会话/未绑定项目目录的场景）：
	// Execute 应跳过项目级解析，直接走基础注册表。
	ctx := WithToolContext(context.Background(), &ToolContext{})

	result, err := tool.Execute(ctx, map[string]any{"name": "bare-base-skill"})
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if m := result.(map[string]any); !strings.Contains(m["content"].(string), "基础库内容") {
		t.Fatalf("无项目目录时应返回基础库内容, 实际: %v", m["content"])
	}
}

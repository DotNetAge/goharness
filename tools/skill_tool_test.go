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

	t.Run("基础库未命中时按会话项目目录回退解析", func(t *testing.T) {
		ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-a")})

		result, err := tool.Execute(ctx, map[string]any{"name": "proj-skill"})
		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		m := result.(map[string]any)
		if !strings.Contains(m["content"].(string), "动态技能") {
			t.Fatalf("应返回动态技能内容, 实际: %v", m["content"])
		}
		if len(resolverCalls) != 1 || resolverCalls[0] != "/tmp/proj-a|proj-skill" {
			t.Fatalf("解析器调用记录不符: %v", resolverCalls)
		}
	})

	t.Run("回退未命中返回未找到错误", func(t *testing.T) {
		ctx := WithToolContext(context.Background(), &ToolContext{Session: newSessionWithProjectDir(t, "/tmp/proj-b")})

		if _, err := tool.Execute(ctx, map[string]any{"name": "never-exists-skill"}); err == nil {
			t.Fatalf("基础库与回退均未命中时应返回未找到错误")
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

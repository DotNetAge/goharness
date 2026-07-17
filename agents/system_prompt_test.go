package agents

import (
	"strings"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSystemPromptsStructure 验证系统提示词包含期望段落。
func TestBuildSystemPromptsStructure(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	msgs := rt.buildSystemPrompts(sess.ID(), sess)
	require.Len(t, msgs, 1)
	assert.Equal(t, "system", msgs[0].Role)

	text := msgText(t, msgs[0])
	assert.Contains(t, text, "## 搜索策略")
	assert.Contains(t, text, "## 环境")
	assert.Contains(t, text, "## 可用工具目录")
	assert.Contains(t, text, "## 沟通风格")
}

// TestBuildSystemPromptsWithAgent 验证智能体身份段落被正确注入。
func TestBuildSystemPromptsWithAgent(t *testing.T) {
	reg := newTestAgentRegistry(t, config.AgentConfig{
		Name:         "test-agent",
		Role:         "测试助手",
		Description:  "用于测试",
		Introduction: "你好，我是测试助手。",
	})

	rt := newTestRuntime(t, WithAgentRegistry(reg))
	sess := newTestSession(t)

	msgs := rt.buildSystemPrompts(sess.ID(), sess)
	text := msgText(t, msgs[0])
	assert.Contains(t, text, "你是 测试助手。")
	assert.Contains(t, text, "用于测试")
	assert.Contains(t, text, "你好，我是测试助手。")
}

// TestBuildSystemPromptsCompactPlaceholder 验证压缩占位符的开关逻辑。
func TestBuildSystemPromptsCompactPlaceholder(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	// maxWindowSize = 0 时（未启用压缩）按实现也会插入占位符
	sess.SetMaxWindowSize(0)
	msgs := rt.buildSystemPrompts(sess.ID(), sess)
	text := msgText(t, msgs[0])
	assert.Contains(t, text, "## 压缩内容")

	// maxWindowSize = 128K 时应插入压缩占位符
	sess.SetMaxWindowSize(128 * 1024)
	msgs = rt.buildSystemPrompts(sess.ID(), sess)
	text = msgText(t, msgs[0])
	assert.Contains(t, text, "## 压缩内容")

	// maxWindowSize > 128K 时不应插入压缩占位符
	sess.SetMaxWindowSize(256 * 1024)
	msgs = rt.buildSystemPrompts(sess.ID(), sess)
	text = msgText(t, msgs[0])
	assert.NotContains(t, text, "## 压缩内容")
}

// TestBuildSystemPromptsCustomBuilders 验证自定义 builder 覆盖生效。
func TestBuildSystemPromptsCustomBuilders(t *testing.T) {
	reg := newTestAgentRegistry(t, config.AgentConfig{
		Name:         "test-agent",
		Role:         "测试助手",
		Skills:       []string{"test-skill"},
		Introduction: "你好",
	})

	skillReg := skill.NewDefaultSkillRegistry()
	require.NoError(t, skillReg.RegisterSkill(&skill.Skill{Name: "test-skill", Description: "测试技能"}))

	rt := newTestRuntime(t,
		WithAgentRegistry(reg),
		WithSkillRegistry(skillReg),
		WithSkillsPrompt(func(_ []*skill.Skill) string { return "CUSTOM_SKILLS" }),
		WithEnvs(func(_ EnvsParams) string { return "CUSTOM_ENVS" }),
		WithSearchStrategy(func() string { return "CUSTOM_SEARCH" }),
	)
	sess := newTestSession(t)

	msgs := rt.buildSystemPrompts(sess.ID(), sess)
	text := msgText(t, msgs[0])
	assert.Contains(t, text, "CUSTOM_SKILLS")
	assert.Contains(t, text, "CUSTOM_ENVS")
	assert.Contains(t, text, "CUSTOM_SEARCH")
}

// TestAssembleMessagesOrder 验证消息顺序与角色映射。
func TestAssembleMessagesOrder(t *testing.T) {
	rt := newTestRuntime(t)
	history := []session.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ToolCalls: []session.ToolCall{{ID: "tc1", Name: "Grep", Arguments: `{}`}}},
		{Role: "tool", Content: "result", ToolCallID: "tc1"},
	}

	msgs := rt.assembleMessages(rt.buildSystemPrompts("sid", newTestSession(t)), history, "follow up")
	require.Len(t, msgs, 5)
	assert.Equal(t, "system", msgs[0].Role)
	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "hello", msgText(t, msgs[1]))
	assert.Equal(t, "assistant", msgs[2].Role)
	assert.Equal(t, "tool", msgs[3].Role)
	assert.Equal(t, "tc1", msgs[3].ToolCallID)
	assert.Equal(t, "user", msgs[4].Role)
	assert.Equal(t, "follow up", msgText(t, msgs[4]))
}

// TestAssembleMessagesDeduplicatesQuestion 验证当历史末尾已有相同用户问题时不再追加。
func TestAssembleMessagesDeduplicatesQuestion(t *testing.T) {
	rt := newTestRuntime(t)
	history := []session.Message{
		{Role: "user", Content: "same question"},
	}

	msgs := rt.assembleMessages(nil, history, "same question")
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "same question", msgText(t, msgs[0]))
}

// TestAssembleMessagesAssistantWithReasoning 验证 assistant 消息的推理内容被保留。
func TestAssembleMessagesAssistantWithReasoning(t *testing.T) {
	rt := newTestRuntime(t)
	history := []session.Message{
		{Role: "assistant", Content: "answer", ReasoningContent: "thinking process"},
	}

	msgs := rt.assembleMessages(nil, history, "")
	require.Len(t, msgs, 1)
	assert.Equal(t, "thinking process", msgs[0].ReasoningContent)
}

// TestStripOrphanedToolCalls 验证孤立 tool_call 被正确过滤。
func TestStripOrphanedToolCalls(t *testing.T) {
	history := []session.Message{
		{Role: "assistant", Content: "", ToolCalls: []session.ToolCall{{ID: "tc1", Name: "Grep", Arguments: `{}`}}},
		{Role: "assistant", Content: "text only", ToolCalls: []session.ToolCall{{ID: "tc2", Name: "Glob", Arguments: `{}`}}},
		{Role: "tool", Content: "result", ToolCallID: "tc2"},
	}

	result := stripOrphanedToolCalls(history)
	require.Len(t, result, 2)
	// 第一条 assistant 消息的 tool_call 没有对应 tool 响应且文本为空，整条消息被丢弃
	// 第二条 assistant 消息的 tool_call 有对应 tool 响应，保留 tool_call
	assert.Equal(t, "text only", result[0].Content)
	require.Len(t, result[0].ToolCalls, 1)
	assert.Equal(t, "tc2", result[0].ToolCalls[0].ID)
	assert.Equal(t, "tool", result[1].Role)
	assert.Equal(t, "tc2", result[1].ToolCallID)
}

// TestStripOrphanedToolCallsEmpty 验证空输入安全。
func TestStripOrphanedToolCallsEmpty(t *testing.T) {
	result := stripOrphanedToolCalls(nil)
	assert.Empty(t, result)
}

// msgText 辅助函数，从 gochatcore.Message 中提取文本内容。
func msgText(t *testing.T, msg gochatcore.Message) string {
	t.Helper()
	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

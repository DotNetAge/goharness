package agents

import (
	"strings"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSystemPromptsStructure 验证 goharness 不生成任何应用语义文案段：
// 输出即 baseBuilder 的结果，无机制段追加。
func TestBuildSystemPromptsStructure(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	msgs := rt.prompt.BuildSystemPrompts(sess.ID(), sess)
	require.Len(t, msgs, 1)
	assert.Equal(t, "system", msgs[0].Role)

	text := msgText(t, msgs[0])
	// 未注入 baseBuilder 时为空 system 消息（保持消息结构稳定，供 Hook 定位锚点）
	assert.Empty(t, text)
	// 应用语义段（身份/公共规则/环境/搜索策略）全部由应用侧生成，goharness 不再产生
	assert.NotContains(t, text, "## 行为准则")
	assert.NotContains(t, text, "## 沟通风格")
	assert.NotContains(t, text, "## 搜索策略")
	assert.NotContains(t, text, "## 环境")
}

// TestBuildSystemPromptsWithBasePrompt 验证应用侧注入的基础提示词被完整透传，
// goharness 不在其后追加任何文案段。
func TestBuildSystemPromptsWithBasePrompt(t *testing.T) {
	rt := newTestRuntime(t,
		WithBaseSystemPrompt(func(_ string, _ *session.Session) string {
			return "我叫 test-agent 是一名 测试助手。你好，我是测试助手。"
		}),
	)
	sess := newTestSession(t)

	msgs := rt.prompt.BuildSystemPrompts(sess.ID(), sess)
	require.Len(t, msgs, 1)
	text := msgText(t, msgs[0])
	// 输出即 base 段原样，无任何追加
	assert.Equal(t, "我叫 test-agent 是一名 测试助手。你好，我是测试助手。", text)
}

// TestBuildSystemPromptsEmptyBasePrompt 验证 baseBuilder 返回空字符串时
// 生成空 system 消息（单条、无内容）。
func TestBuildSystemPromptsEmptyBasePrompt(t *testing.T) {
	rt := newTestRuntime(t,
		WithBaseSystemPrompt(func(_ string, _ *session.Session) string {
			return ""
		}),
	)
	sess := newTestSession(t)

	msgs := rt.prompt.BuildSystemPrompts(sess.ID(), sess)
	require.Len(t, msgs, 1)
	assert.Empty(t, msgText(t, msgs[0]))
}

// TestAssembleMessagesOrder 验证消息顺序与角色映射。
func TestAssembleMessagesOrder(t *testing.T) {
	rt := newTestRuntime(t)
	history := []session.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ToolCalls: []session.ToolCall{{ID: "tc1", Name: "Grep", Arguments: `{}`}}},
		{Role: "tool", Content: "result", ToolCallID: "tc1"},
	}

	msgs := AssembleMessages(rt.prompt.BuildSystemPrompts("sid", newTestSession(t)), history, "follow up")
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
	history := []session.Message{
		{Role: "user", Content: "same question"},
	}

	msgs := AssembleMessages(nil, history, "same question")
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "same question", msgText(t, msgs[0]))
}

// TestAssembleMessagesAssistantWithReasoning 验证 assistant 消息的推理内容被保留。
func TestAssembleMessagesAssistantWithReasoning(t *testing.T) {
	history := []session.Message{
		{Role: "assistant", Content: "answer", ReasoningContent: "thinking process"},
	}

	msgs := AssembleMessages(nil, history, "")
	require.Len(t, msgs, 1)
	assert.Equal(t, "thinking process", msgs[0].ReasoningContent)
}

// TestAssembleMessagesImageBlocks 验证携带图片的用户消息被组装为
// 多模态消息（文本块 + 图片内容块），图片以 image_url 消息形式进入上下文。
// 内容块类型使用 ContentTypeImage（而非 ContentTypeImageURL）：
// gochat 的 ollama 客户端只识别 ContentTypeImage；OpenAI 转换端对两者都支持，
// 统一使用 ContentTypeImage 可同时兼容两个提供商。
func TestAssembleMessagesImageBlocks(t *testing.T) {
	history := []session.Message{
		{
			Role: "user", Content: "请分析这张图",
			Images: []session.ImageBlock{
				{MediaType: "image/png", Base64Data: "aGVsbG8=", AltText: "512x300"},
			},
		},
	}

	msgs := AssembleMessages(nil, history, "")
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	require.Len(t, msgs[0].Content, 2)
	assert.Equal(t, gochatcore.ContentTypeText, msgs[0].Content[0].Type)
	assert.Equal(t, "请分析这张图", msgs[0].Content[0].Text)
	assert.Equal(t, gochatcore.ContentTypeImage, msgs[0].Content[1].Type)
	assert.Equal(t, "image/png", msgs[0].Content[1].MediaType)
	assert.Equal(t, "aGVsbG8=", msgs[0].Content[1].Data)
}

// TestAssembleMessagesUserWithoutImages 验证普通用户消息仍为单文本块。
func TestAssembleMessagesUserWithoutImages(t *testing.T) {
	history := []session.Message{
		{Role: "user", Content: "plain text"},
	}

	msgs := AssembleMessages(nil, history, "")
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].Content, 1)
	assert.Equal(t, "plain text", msgText(t, msgs[0]))
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

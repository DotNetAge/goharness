package reactor

import (
	"fmt"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// ConversationHistory 是历史消息列表的类型别名。
type ConversationHistory = []core.Message

// LLMResponse 是单次 LLM 调用的原始输出。
// 这不是决策结构体 —— 只是 LLM 返回的数据。
// 工具调用列表为空表示 LLM 直接回答了。
type LLMResponse struct {
	Content      string                // 文本输出
	Reasoning    string                // 思考内容（如有）
	FinishReason string                // 原生 stop_reason
	ToolCalls    []ToolCallInvocation  // 工具调用列表，空 = 直接回答
	TokenUsage   core.TokenUsage
	AbortReason  string                // 非空 = Hook 中止
}

// ToolCallInvocation 是 LLM 请求的单个工具调用。
type ToolCallInvocation struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult 是单个工具调用的执行结果。
type ToolResult struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Result     string        `json:"result,omitempty"`
	Metadata   any           `json:"metadata,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ms"`
	Success    bool          `json:"success"`
}

// ToolResultSummary 返回工具结果的摘要字符串。
func (tr ToolResult) ToolResultSummary() string {
	prefix := fmt.Sprintf("[%s]", tr.ToolName)
	if tr.Error != "" {
		return fmt.Sprintf("%s error: %s", prefix, tr.Error)
	}
	if tr.Result != "" {
		truncated := truncateStr(tr.Result, 200)
		return fmt.Sprintf("%s returned: %s", prefix, truncated)
	}
	return fmt.Sprintf("%s returned: (empty result)", prefix)
}

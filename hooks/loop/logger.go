package loop

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// LoopLoggerHook 记录 Think-Act 循环中每次 LLM 调用的开始和结束。
type LoopLoggerHook struct {
	Logger logging.Logger
}

func (h *LoopLoggerHook) Priority() int { return hooks.PriorityLoopLogger }

// BeforeLLM 记录 LLM 调用的开始，包含 session ID、迭代序号和输入预览。
func (h *LoopLoggerHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	if h.Logger == nil {
		return hooks.HookResult{}
	}
	h.Logger.Info("llm call start",
		"session_id", sessionID,
		"iteration", iteration+1,
		"input_preview", hooks.Truncate(input.UserMessage, 80),
	)
	return hooks.HookResult{}
}

// AfterLLM 记录 LLM 调用的完成，包含工具调用信息。
func (h *LoopLoggerHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	if h.Logger == nil {
		return hooks.HookResult{}
	}
	hasTools := len(resp.ToolCalls) > 0
	h.Logger.Info("llm call done",
		"session_id", sessionID,
		"iteration", iteration+1,
		"has_tools", hasTools,
		"tool_count", len(resp.ToolCalls),
	)
	return hooks.HookResult{}
}

// Abort 对 LoopLoggerHook 是空操作。
func (h *LoopLoggerHook) Abort(sessionID string, reason string) {}

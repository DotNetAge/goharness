package loop
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
)

// LoopLoggerHook 记录循环开始和结束日志。
type LoopLoggerHook struct {
	Logger logging.Logger
}

func (h *LoopLoggerHook) Priority() int { return hooks.PriorityLoopLogger }

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

func (h *LoopLoggerHook) Abort(sessionID string, reason string) {}

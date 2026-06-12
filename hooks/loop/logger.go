package loop

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// LoopLoggerHook logs the start and end of each LLM call in the Think-Act loop.
type LoopLoggerHook struct {
	Logger logging.Logger
}

func (h *LoopLoggerHook) Priority() int { return hooks.PriorityLoopLogger }

// BeforeLLM logs the start of an LLM call with session ID, iteration, and input preview.
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

// AfterLLM logs the completion of an LLM call with tool call information.
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

// Abort is a no-op for LoopLoggerHook.
func (h *LoopLoggerHook) Abort(sessionID string, reason string) {}

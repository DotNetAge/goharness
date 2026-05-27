package loop

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// LoopLoggerHook 记录循环开始和结束日志。
type LoopLoggerHook struct {
	Logger core.Logger
}

func (h *LoopLoggerHook) Priority() int { return reactor.PriorityLoopLogger }

func (h *LoopLoggerHook) BeforeLLM(sessionID string, iteration int, input *reactor.CallInput) reactor.HookResult {
	if h.Logger == nil {
		return reactor.HookResult{}
	}
	h.Logger.Info("llm call start",
		"session_id", sessionID,
		"iteration", iteration+1,
		"input_preview", reactor.Truncate(input.UserMessage, 80),
	)
	return reactor.HookResult{}
}

func (h *LoopLoggerHook) AfterLLM(sessionID string, iteration int, resp *reactor.LLMResponse, results []reactor.ToolResult) reactor.HookResult {
	if h.Logger == nil {
		return reactor.HookResult{}
	}
	hasTools := len(resp.ToolCalls) > 0
	h.Logger.Info("llm call done",
		"session_id", sessionID,
		"iteration", iteration+1,
		"has_tools", hasTools,
		"tool_count", len(resp.ToolCalls),
	)
	return reactor.HookResult{}
}

func (h *LoopLoggerHook) Abort(sessionID string, reason string) {}

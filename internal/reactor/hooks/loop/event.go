package loop

import (
	"github.com/DotNetAge/goreact/reactor"
)

// LoopEventHook 在循环结束后发射 ThinkingDone 事件。
type LoopEventHook struct{}

func (h *LoopEventHook) Priority() int { return reactor.PriorityLoopEvent }

func (h *LoopEventHook) BeforeLLM(sessionID string, iteration int, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *LoopEventHook) AfterLLM(sessionID string, iteration int, resp *reactor.LLMResponse, results []reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *LoopEventHook) Abort(sessionID string, reason string) {}

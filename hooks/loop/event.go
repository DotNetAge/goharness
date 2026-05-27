package loop

import (
	"github.com/DotNetAge/goreact/hooks"
)

// LoopEventHook emits lifecycle events for the Think-Act loop.
// It can be extended to emit specific events at different loop phases.
type LoopEventHook struct{}

func (h *LoopEventHook) Priority() int { return hooks.PriorityLoopEvent }

// BeforeLLM is a no-op for LoopEventHook.
func (h *LoopEventHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

// AfterLLM is a no-op for LoopEventHook.
func (h *LoopEventHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for LoopEventHook.
func (h *LoopEventHook) Abort(sessionID string, reason string) {}

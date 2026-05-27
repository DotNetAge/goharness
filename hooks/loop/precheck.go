package loop

import (
	"github.com/DotNetAge/goreact/hooks"
)

// PreCheckHook checks termination conditions before each loop iteration.
// It can be extended to implement custom termination logic.
type PreCheckHook struct{}

func (h *PreCheckHook) Priority() int { return hooks.PriorityPreCheck }

// BeforeLLM is a no-op for PreCheckHook.
func (h *PreCheckHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

// AfterLLM is a no-op for PreCheckHook.
func (h *PreCheckHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for PreCheckHook.
func (h *PreCheckHook) Abort(sessionID string, reason string) {}

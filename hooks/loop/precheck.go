package loop
import (
	"github.com/DotNetAge/goreact/hooks"
)

// PreCheckHook 在每次循环开始时检查终止条件。
type PreCheckHook struct{}

func (h *PreCheckHook) Priority() int { return hooks.PriorityPreCheck }

func (h *PreCheckHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *PreCheckHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *PreCheckHook) Abort(sessionID string, reason string) {}

package loop
import (
	"github.com/DotNetAge/goreact/hooks"
)

// LoopEventHook 在循环结束后发射 ThinkingDone 事件。
type LoopEventHook struct{}

func (h *LoopEventHook) Priority() int { return hooks.PriorityLoopEvent }

func (h *LoopEventHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *LoopEventHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *LoopEventHook) Abort(sessionID string, reason string) {}

package loop

import (
	"github.com/DotNetAge/goreact/reactor"
)

// PreCheckHook 在每次循环开始时检查终止条件。
type PreCheckHook struct{}

func (h *PreCheckHook) Priority() int { return reactor.PriorityPreCheck }

func (h *PreCheckHook) BeforeLLM(sessionID string, iteration int, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *PreCheckHook) AfterLLM(sessionID string, iteration int, resp *reactor.LLMResponse, results []reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *PreCheckHook) Abort(sessionID string, reason string) {}

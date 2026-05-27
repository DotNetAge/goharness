package loop

import (
	"github.com/DotNetAge/goreact/hooks"
)

// ConvergenceHook 在每次循环后检查是否应该终止。
type ConvergenceHook struct{}

func (h *ConvergenceHook) Priority() int { return hooks.PriorityConvergence }

func (h *ConvergenceHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *ConvergenceHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	for _, tr := range results {
		if !tr.Success && tr.Error != "" {
			if isIrrecoverable(tr.Error) {
				return hooks.HookResult{Abort: true, AbortReason: "irrecoverable tool error: " + tr.Error}
			}
		}
	}
	return hooks.HookResult{}
}

func (h *ConvergenceHook) Abort(sessionID string, reason string) {}

func isIrrecoverable(errStr string) bool {
	if errStr == "" {
		return false
	}
	patterns := []string{
		"permission denied", "unauthorized", "invalid api key",
		"authentication failed", "access denied", "forbidden",
	}
	lower := errStr
	for _, p := range patterns {
		if containsLower(lower, p) {
			return true
		}
	}
	return false
}

func containsLower(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			if sc >= 'A' && sc <= 'Z' {
				sc = sc + 32
			}
			if sc != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

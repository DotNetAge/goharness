package loop

import (
	"github.com/DotNetAge/goharness/hooks"
)

// ConvergenceHook 在每次循环迭代后检查是否应因工具执行结果中的不可恢复错误而终止循环。
type ConvergenceHook struct{}

func (h *ConvergenceHook) Priority() int { return hooks.PriorityConvergence }

// BeforeLLM 对 ConvergenceHook 是空操作。
func (h *ConvergenceHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{}
}

// AfterLLM 检查工具结果中是否包含应终止循环的不可恢复错误。
func (h *ConvergenceHook) AfterLLM(sessionID string, iteration int, resp *hooks.LLMResponse, results []hooks.ToolResult) hooks.HookResult {
	for _, tr := range results {
		if !tr.Success && tr.Error != "" {
			if isIrrecoverable(tr.Error) {
				return hooks.HookResult{Abort: true, AbortReason: "不可恢复的工具错误: " + tr.Error}
			}
		}
	}
	return hooks.HookResult{}
}

// Abort 对 ConvergenceHook 是空操作。
func (h *ConvergenceHook) Abort(sessionID string, reason string) {}

// isIrrecoverable 检查错误字符串是否表示不可恢复的失败。
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

// containsLower 执行不区分大小写的子串检查。
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

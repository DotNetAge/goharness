package thought

import (
	"github.com/DotNetAge/goreact/reactor"
)

// ConvergenceHook 在每次 Think 阶段后检查循环是否应该终止。
// 检测循环卡死模式：工具循环、错误循环、振荡、无进展等。
// 如果 LLM 直接回答了（ToolCalls 为空），终止由 runLoop 处理，不在本钩子中重复检查。
type ConvergenceHook struct{}

func (h *ConvergenceHook) Priority() int { return reactor.PriorityConvergence }

func (h *ConvergenceHook) Before(ctx *reactor.ReactContext, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *ConvergenceHook) After(ctx *reactor.ReactContext, thought *reactor.Thought) reactor.HookResult {
	// LLM 直接回答了 → runLoop 会处理终止，这里不重复 abort
	// 只检查 stuck 模式

	if reactor.IsDestructiveLoop(ctx.History) {
		return reactor.HookResult{Abort: true, AbortReason: "destructive loop detected"}
	}
	if reactor.IsAgentStuck(ctx.History) {
		return reactor.HookResult{Abort: true, AbortReason: "agent stuck"}
	}
	if reactor.IsResultConverged(ctx.History) {
		return reactor.HookResult{Abort: true, AbortReason: "result converged"}
	}
	if reactor.IsDuplicateAction(ctx.History) {
		return reactor.HookResult{Abort: true, AbortReason: "duplicate action detected"}
	}
	// 检测不可恢复的工具错误
	if ctx.LastAction != nil {
		for _, tr := range ctx.LastAction.Results {
			if !tr.Success && tr.Error != "" {
				if isIrrecoverable(tr.Error) {
					return reactor.HookResult{Abort: true, AbortReason: "irrecoverable tool error: " + tr.Error}
				}
			}
		}
	}
	return reactor.HookResult{}
}

func (h *ConvergenceHook) Abort(ctx *reactor.ReactContext, reason string) {}

// isIrrecoverable 检查工具错误是否不可恢复。
func isIrrecoverable(errStr string) bool {
	if errStr == "" {
		return false
	}
	patterns := []string{
		"permission denied",
		"unauthorized",
		"invalid api key",
		"authentication failed",
		"access denied",
		"forbidden",
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

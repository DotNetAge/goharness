package observation

import (
	"strings"

	"github.com/DotNetAge/goreact/reactor"
)

// ConvergenceHook 在观察阶段收尾时判定循环是否应该终止。
type ConvergenceHook struct{}

func (h *ConvergenceHook) Priority() int { return reactor.PriorityConvergence }

func (h *ConvergenceHook) After(ctx *reactor.ReactContext, obs *reactor.Observation) reactor.HookResult {
	thought := ctx.LastThought
	if thought != nil {
		if thought.IsFinal {
			return reactor.HookResult{Abort: true, AbortReason: "thinker produced final answer"}
		}
		if thought.Decision == reactor.DecisionAnswer {
			return reactor.HookResult{Abort: true, AbortReason: "direct answer produced"}
		}
		if thought.Decision == reactor.DecisionClarify {
			return reactor.HookResult{Abort: true, AbortReason: "clarification needed"}
		}
	}

	if !obs.Success && !obs.ShouldRetry && obs.Error != "" {
		if isIrrecoverable(obs.Error) {
			return reactor.HookResult{Abort: true, AbortReason: "irrecoverable tool error: " + obs.Error}
		}
	}

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
	lower := strings.ToLower(errStr)
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

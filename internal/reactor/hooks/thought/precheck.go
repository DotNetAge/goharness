package thought

import (
	"github.com/DotNetAge/goreact/reactor"
)

// PreCheckHook 在每次循环开始时检查 MaxIterations 和 ctx cancelled。
type PreCheckHook struct{}

func (h *PreCheckHook) Priority() int { return reactor.PriorityPreCheck }

func (h *PreCheckHook) Before(ctx *reactor.ReactContext, input *reactor.CallInput) reactor.HookResult {
	if ctx.TerminationReason != "" {
		return reactor.HookResult{Abort: true, AbortReason: ctx.TerminationReason}
	}
	if ctx.CurrentIteration >= ctx.MaxIterations {
		return reactor.HookResult{Abort: true, AbortReason: "reached max iterations"}
	}
	if ctx.Ctx().Err() != nil {
		return reactor.HookResult{Abort: true, AbortReason: "request cancelled"}
	}
	return reactor.HookResult{}
}

func (h *PreCheckHook) After(ctx *reactor.ReactContext, thought *reactor.Thought) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *PreCheckHook) Abort(ctx *reactor.ReactContext, reason string) {}

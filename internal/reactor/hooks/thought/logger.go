package thought

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ThoughtLoggerHook 记录 Think 阶段开始和结束日志。
type ThoughtLoggerHook struct {
	Logger core.Logger
}

func (h *ThoughtLoggerHook) Priority() int { return reactor.PriorityThoughtLogger }

func (h *ThoughtLoggerHook) Before(ctx *reactor.ReactContext, input *reactor.CallInput) reactor.HookResult {
	if h.Logger == nil {
		return reactor.HookResult{}
	}
	h.Logger.Info("think start",
		"session_id", ctx.SessionID,
		"iteration", ctx.CurrentIteration+1,
		"input_preview", reactor.Truncate(ctx.Input, 80),
	)
	return reactor.HookResult{}
}

func (h *ThoughtLoggerHook) After(ctx *reactor.ReactContext, thought *reactor.Thought) reactor.HookResult {
	if h.Logger == nil {
		return reactor.HookResult{}
	}
	h.Logger.Info("think done",
		"session_id", ctx.SessionID,
		"iteration", ctx.CurrentIteration+1,
		"decision", thought.Decision,
	)
	return reactor.HookResult{}
}

func (h *ThoughtLoggerHook) Abort(ctx *reactor.ReactContext, reason string) {}

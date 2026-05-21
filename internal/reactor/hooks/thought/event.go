package thought

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ThoughtEventHook 在思考阶段完成后发射 ThinkingDone 事件。
type ThoughtEventHook struct{}

func (h *ThoughtEventHook) Priority() int { return reactor.PriorityThoughtEvent }

func (h *ThoughtEventHook) Before(ctx *reactor.ReactContext, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *ThoughtEventHook) After(ctx *reactor.ReactContext, thought *reactor.Thought) reactor.HookResult {
	ctx.EmitEvent(core.ThinkingDone, thought)
	return reactor.HookResult{}
}

func (h *ThoughtEventHook) Abort(ctx *reactor.ReactContext, reason string) {}

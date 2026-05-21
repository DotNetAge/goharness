package observation

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ObservationEventHook 在观察阶段完成后发射 ObservationDone 事件。
type ObservationEventHook struct{}

func (h *ObservationEventHook) Priority() int { return reactor.PriorityObservationEvent }

func (h *ObservationEventHook) After(ctx *reactor.ReactContext, obs *reactor.Observation) reactor.HookResult {
	ctx.EmitEvent(core.ObservationDone, obs)
	return reactor.HookResult{}
}

func (h *ObservationEventHook) Abort(ctx *reactor.ReactContext, reason string) {}

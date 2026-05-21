package observation

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ObservationLoggerHook 记录观察阶段的成功/失败日志。
type ObservationLoggerHook struct {
	Logger core.Logger
}

func (h *ObservationLoggerHook) Priority() int { return reactor.PriorityObservationLogger }

func (h *ObservationLoggerHook) After(ctx *reactor.ReactContext, obs *reactor.Observation) reactor.HookResult {
	if !obs.Success && obs.Error != "" {
		h.Logger.Warn("observe error",
			"session_id", ctx.SessionID,
			"error", obs.Error,
		)
	} else {
		h.Logger.Info("observe success",
			"session_id", ctx.SessionID,
			"insights", len(obs.Insights),
		)
	}
	return reactor.HookResult{}
}

func (h *ObservationLoggerHook) Abort(ctx *reactor.ReactContext, reason string) {}

package action

import (
	"fmt"

	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ToolLoggerHook 记录每个工具的开始、成功、失败日志。
type ToolLoggerHook struct {
	Logger core.Logger
}

func (h *ToolLoggerHook) Priority() int { return reactor.PriorityToolLogger }

func (h *ToolLoggerHook) Before(ctx *reactor.ReactContext, toolName string, params map[string]any) reactor.HookResult {
	h.Logger.Info("tool start",
		"session_id", ctx.SessionID,
		"tool", toolName,
	)
	return reactor.HookResult{}
}

func (h *ToolLoggerHook) After(ctx *reactor.ReactContext, result *reactor.ToolResult) reactor.HookResult {
	if result.Error != "" {
		h.Logger.Error("tool error", fmt.Errorf("%s", result.Error),
			"session_id", ctx.SessionID,
			"tool", result.ToolName,
		)
	} else {
		h.Logger.Info("tool done",
			"session_id", ctx.SessionID,
			"tool", result.ToolName,
		)
	}
	return reactor.HookResult{}
}

func (h *ToolLoggerHook) Abort(ctx *reactor.ReactContext, reason string) {}

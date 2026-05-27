package action

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ToolLoggerHook 记录工具执行开始和结束日志。
type ToolLoggerHook struct {
	Logger core.Logger
}

func (h *ToolLoggerHook) Priority() int { return reactor.PriorityToolLogger }

func (h *ToolLoggerHook) Before(sessionID string, toolName string, params map[string]any) reactor.HookResult {
	if h.Logger == nil { return reactor.HookResult{} }
	h.Logger.Debug("tool start", "session_id", sessionID, "tool", toolName)
	return reactor.HookResult{}
}

func (h *ToolLoggerHook) After(result *reactor.ToolResult) reactor.HookResult {
	if h.Logger == nil { return reactor.HookResult{} }
	h.Logger.Debug("tool done",
		"tool", result.ToolName,
		"success", result.Success,
		"duration_ms", result.Duration.Milliseconds(),
	)
	return reactor.HookResult{}
}

func (h *ToolLoggerHook) Abort(reason string) {}

package action

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// ToolLoggerHook 记录每次工具执行的开始和结束。
type ToolLoggerHook struct {
	Logger logging.Logger
}

func (h *ToolLoggerHook) Priority() int { return hooks.PriorityToolLogger }

// Before 记录工具执行的开始，包含 session ID 和工具名。
func (h *ToolLoggerHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	if h.Logger == nil {
		return hooks.HookResult{}
	}
	h.Logger.Debug("tool start", "session_id", sessionID, "tool", toolName)
	return hooks.HookResult{}
}

// After 记录工具执行的完成，包含成功状态和耗时。
func (h *ToolLoggerHook) After(result *hooks.ToolResult) hooks.HookResult {
	if h.Logger == nil {
		return hooks.HookResult{}
	}
	h.Logger.Debug("tool done",
		"tool", result.ToolName,
		"success", result.Success,
		"duration_ms", result.Duration.Milliseconds(),
	)
	return hooks.HookResult{}
}

// Abort 对 ToolLoggerHook 是空操作。
func (h *ToolLoggerHook) Abort(reason string) {}

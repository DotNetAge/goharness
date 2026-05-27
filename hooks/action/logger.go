package action
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
)

// ToolLoggerHook logs the start and end of each tool execution.
type ToolLoggerHook struct {
	Logger logging.Logger
}

func (h *ToolLoggerHook) Priority() int { return hooks.PriorityToolLogger }

// Before logs the start of a tool execution with session ID and tool name.
func (h *ToolLoggerHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	if h.Logger == nil {
		return hooks.HookResult{}
	}
	h.Logger.Debug("tool start", "session_id", sessionID, "tool", toolName)
	return hooks.HookResult{}
}

// After logs the completion of a tool execution with success status and duration.
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

// Abort is a no-op for ToolLoggerHook.
func (h *ToolLoggerHook) Abort(reason string) {}

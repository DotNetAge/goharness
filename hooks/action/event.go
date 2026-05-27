package action

import (
	"github.com/DotNetAge/goreact/hooks"
)

// ToolEventHook emits ToolExecStart and ToolExecEnd events for tool executions.
type ToolEventHook struct{}

func (h *ToolEventHook) Priority() int { return hooks.PriorityToolEvent }

// Before is a no-op for ToolEventHook.
func (h *ToolEventHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	return hooks.HookResult{}
}

// After is a no-op for ToolEventHook.
func (h *ToolEventHook) After(result *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for ToolEventHook.
func (h *ToolEventHook) Abort(reason string) {}

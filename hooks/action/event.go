package action
import (
	"github.com/DotNetAge/goreact/hooks"
)

// ToolEventHook emits ToolExecStart/ToolExecEnd events.
type ToolEventHook struct{}

func (h *ToolEventHook) Priority() int { return hooks.PriorityToolEvent }

func (h *ToolEventHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *ToolEventHook) After(result *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *ToolEventHook) Abort(reason string) {}

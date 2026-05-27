package action

import (
	"github.com/DotNetAge/goreact/reactor"
)

// ToolEventHook emits ToolExecStart/ToolExecEnd events.
type ToolEventHook struct{}

func (h *ToolEventHook) Priority() int { return reactor.PriorityToolEvent }

func (h *ToolEventHook) Before(sessionID string, toolName string, params map[string]any) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *ToolEventHook) After(result *reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *ToolEventHook) Abort(reason string) {}

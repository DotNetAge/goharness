package action

import (
	"fmt"

	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

type PermissionHook struct {
	Chain  core.PermissionChain
	Logger core.Logger
}

func (h *PermissionHook) Priority() int { return reactor.PriorityPermission }

func (h *PermissionHook) Before(sessionID string, toolName string, params map[string]any) reactor.HookResult {
	if h.Chain == nil {
		return reactor.HookResult{}
	}
	toolCtx := &core.ToolUseContext{
		SessionID: sessionID,
		ToolName:  toolName,
		Params:    params,
	}
	decision, err := h.Chain.Check(toolCtx)
	if err != nil {
		if h.Logger != nil { h.Logger.Error("[permission_hook] check failed", err, "tool", toolName) }
		return reactor.HookResult{Error: fmt.Errorf("permission check failed: %w", err)}
	}
	if h.Logger != nil {
		h.Logger.Debug("[permission_hook] decision", "tool", toolName, "behavior", decision.Behavior, "msg", decision.Message)
	}
	if decision.Behavior == core.PermissionDeny {
		return reactor.HookResult{Abort: true, AbortReason: "permission denied: " + decision.Message}
	}
	return reactor.HookResult{}
}

func (h *PermissionHook) After(result *reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *PermissionHook) Abort(reason string) {}

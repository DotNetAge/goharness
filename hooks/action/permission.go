package action

import (
	"fmt"

	"github.com/DotNetAge/goharness/hooks"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/permission"
	"github.com/DotNetAge/goharness/tools"
)

// PermissionHook checks tool execution permissions before allowing tool calls.
// It uses a permission chain to evaluate whether a tool should be allowed to execute.
type PermissionHook struct {
	Chain  permission.PermissionChain
	Logger logging.Logger
}

func (h *PermissionHook) Priority() int { return hooks.PriorityPermission }

// Before checks if the tool execution is permitted by the permission chain.
// Returns an abort result if permission is denied, or an error if the check fails.
func (h *PermissionHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	if h.Chain == nil {
		return hooks.HookResult{}
	}
	toolCtx := &tools.ToolUseContext{
		SessionID: sessionID,
		ToolName:  toolName,
		Params:    params,
	}
	decision, err := h.Chain.Check(toolCtx)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Error("[permission_hook] check failed", err, "tool", toolName)
		}
		return hooks.HookResult{Error: fmt.Errorf("permission check failed: %w", err)}
	}
	if h.Logger != nil {
		h.Logger.Debug("[permission_hook] decision", "tool", toolName, "behavior", decision.Behavior, "msg", decision.Message)
	}
	if decision.Behavior == permission.PermissionDeny {
		return hooks.HookResult{Abort: true, AbortReason: "permission denied: " + decision.Message}
	}
	return hooks.HookResult{}
}

// After is a no-op for PermissionHook.
func (h *PermissionHook) After(result *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for PermissionHook.
func (h *PermissionHook) Abort(reason string) {}

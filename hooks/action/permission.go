package action
import (
	"github.com/DotNetAge/goreact/hooks"
	"fmt"

	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/permission"
	"github.com/DotNetAge/goreact/tools"
)

type PermissionHook struct {
	Chain  permission.PermissionChain
	Logger logging.Logger
}

func (h *PermissionHook) Priority() int { return hooks.PriorityPermission }

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

func (h *PermissionHook) After(result *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *PermissionHook) Abort(reason string) {}

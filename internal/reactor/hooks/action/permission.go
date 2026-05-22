package action

import (
	"fmt"

	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// PermissionHook 对每个工具调用执行 Allow/Deny 权限检查。
// 实现 ToolHook，只在 Before 中有实际逻辑。
type PermissionHook struct {
	Chain core.PermissionChain
}

func (h *PermissionHook) Priority() int { return reactor.PriorityPermission }

func (h *PermissionHook) Before(ctx *reactor.ReactContext, toolName string, params map[string]any) reactor.HookResult {
	if h.Chain == nil {
		return reactor.HookResult{}
	}
	toolCtx := &core.ToolUseContext{
		SessionID: ctx.SessionID,
		TaskID:    ctx.TaskID,
		ToolName:  toolName,
		Params:    params,
		Iteration: ctx.CurrentIteration,
		Ctx:       ctx.Ctx(),
	}
	decision, err := h.Chain.Check(toolCtx)
	if err != nil {
		return reactor.HookResult{Error: fmt.Errorf("permission check failed: %w", err)}
	}
	if decision.Behavior == core.PermissionDeny {
		// Emit PermissionDenied event at the Hook level so subscribers
		// see it even though the tool never reaches defaultToolExecutor.Execute
		// (which also emits PermissionDenied for its own internal check).
		ctx.EmitEvent(core.PermissionDenied, decision.Message)
		return reactor.HookResult{Abort: true, AbortReason: "permission denied: " + decision.Message}
	}
	return reactor.HookResult{}
}

func (h *PermissionHook) After(ctx *reactor.ReactContext, result *reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *PermissionHook) Abort(ctx *reactor.ReactContext, reason string) {}

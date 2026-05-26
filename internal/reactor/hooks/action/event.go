package action

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ToolEventHook 在工具执行过程中发射 ToolExecStart/ToolExecEnd 事件。
type ToolEventHook struct{}

func (h *ToolEventHook) Priority() int { return reactor.PriorityToolEvent }

func (h *ToolEventHook) Before(ctx *reactor.ReactContext, toolName string, params map[string]any) reactor.HookResult {
	ctx.EmitEvent(core.ToolExecStart, core.ToolExecStartData{
		ToolName: toolName,
		Params:   params,
	})
	return reactor.HookResult{}
}

func (h *ToolEventHook) After(ctx *reactor.ReactContext, result *reactor.ToolResult) reactor.HookResult {
	ctx.EmitEvent(core.ToolExecEnd, core.ToolExecEndData{
		ToolName:   result.ToolName,
		ToolCallID: result.ToolCallID,
		Duration:   result.Duration,
		Success:    result.Success,
		Result:     result.Result,
		Error:      result.Error,
	})
	return reactor.HookResult{}
}

func (h *ToolEventHook) Abort(ctx *reactor.ReactContext, reason string) {}

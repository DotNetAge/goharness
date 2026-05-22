package action

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// ToolEventHook 在工具执行过程中发射 ActionStart/ToolExecStart/ToolExecEnd/ActionProgress 事件。
type ToolEventHook struct {
	totalTools   int
	completed    int
	lastIter     int
	emitStartFor int
}

func (h *ToolEventHook) Priority() int { return reactor.PriorityToolEvent }

func (h *ToolEventHook) Before(ctx *reactor.ReactContext, toolName string, params map[string]any) reactor.HookResult {
	iter := ctx.CurrentIteration
	if iter != h.lastIter {
		h.lastIter = iter
		h.totalTools = 0
		h.completed = 0
		h.emitStartFor = 0
	}
	if h.emitStartFor == 0 && ctx.LastThought != nil {
		toolCount := 0
		var toolNames []string

		// Prefer ToolCallList (ordered, supports same-name parallel calls)
		if len(ctx.LastThought.ToolCallList) > 0 {
			toolCount = len(ctx.LastThought.ToolCallList)
			toolNames = make([]string, 0, toolCount)
			for _, item := range ctx.LastThought.ToolCallList {
				toolNames = append(toolNames, item.Name)
			}
		} else if len(ctx.LastThought.ToolCalls) > 0 {
			// Fallback to ToolCalls map (backward compat for deserialized JSON)
			toolCount = len(ctx.LastThought.ToolCalls)
			toolNames = make([]string, 0, toolCount)
			for name := range ctx.LastThought.ToolCalls {
				toolNames = append(toolNames, name)
			}
		}

		h.totalTools = toolCount
		h.emitStartFor = toolCount

		if toolCount > 0 {
			ctx.EmitEvent(core.ActionStart, core.ActionStartData{
				ToolCount: toolCount,
				ToolNames: toolNames,
				Iteration: iter,
			})
		}
	}

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

	h.completed++
	status := "in_progress"
	if h.completed >= h.totalTools {
		status = "completed"
	}
	ctx.EmitEvent(core.ActionProgress, core.ActionProgressData{
		CompletedCount: h.completed,
		TotalCount:     h.totalTools,
		Status:         status,
	})
	return reactor.HookResult{}
}

func (h *ToolEventHook) Abort(ctx *reactor.ReactContext, reason string) {}

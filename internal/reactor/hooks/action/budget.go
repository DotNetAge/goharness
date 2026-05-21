package action

import (
	"github.com/DotNetAge/goreact/reactor"
)

// BudgetHook 每个工具结果产生后扣减预算（Enforce）。
type BudgetHook struct {
	Enforcer *reactor.ToolResultBudgetEnforcer
}

func (h *BudgetHook) Priority() int { return reactor.PriorityBudget }

func (h *BudgetHook) Before(ctx *reactor.ReactContext, toolName string, params map[string]any) reactor.HookResult {
	return reactor.HookResult{}
}

func (h *BudgetHook) After(ctx *reactor.ReactContext, result *reactor.ToolResult) reactor.HookResult {
	if h.Enforcer != nil {
		results := []reactor.ToolResult{*result}
		h.Enforcer.Enforce(results)
		*result = results[0]
	}
	return reactor.HookResult{}
}

func (h *BudgetHook) Abort(ctx *reactor.ReactContext, reason string) {}

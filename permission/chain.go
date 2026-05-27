package permission

import "github.com/DotNetAge/goreact/tools"

type PermissionDecision struct {
	Behavior     PermissionBehavior
	Message      string
	UpdatedInput map[string]any
}

type PermissionChain interface {
	Check(ctx *tools.ToolUseContext) (*PermissionDecision, error)
}

type chainPermissionChain struct {
	checkers []tools.ToolPermissionChecker
}

func NewPermissionChain(checkers ...tools.ToolPermissionChecker) PermissionChain {
	return &chainPermissionChain{checkers: checkers}
}

func (c *chainPermissionChain) Check(ctx *tools.ToolUseContext) (*PermissionDecision, error) {
	for _, checker := range c.checkers {
		result := checker.CheckPermissions(ctx)
		if result.Behavior != PermissionAllow {
			return &PermissionDecision{
				Behavior:     result.Behavior,
				Message:      result.Message,
				UpdatedInput: result.UpdatedInput,
			}, nil
		}
		if result.UpdatedInput != nil {
			ctx.Params = result.UpdatedInput
		}
	}
	return &PermissionDecision{Behavior: PermissionAllow}, nil
}

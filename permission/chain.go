package permission

import "github.com/DotNetAge/goreact/tools"

// PermissionDecision represents the final decision from a permission chain check.
// It includes the behavior determination, an optional message, and any
// modifications to the input parameters made by intermediate checkers.
type PermissionDecision struct {
	Behavior     PermissionBehavior
	Message      string
	UpdatedInput map[string]any
}

// PermissionChain defines the interface for chaining multiple permission checkers.
// Checkers are evaluated in order; the first non-allow result short-circuits the chain.
type PermissionChain interface {
	Check(ctx *tools.ToolUseContext) (*PermissionDecision, error)
}

// chainPermissionChain implements PermissionChain by composing multiple checkers.
type chainPermissionChain struct {
	checkers []tools.ToolPermissionChecker
}

// NewPermissionChain creates a new permission chain from the given checkers.
// Checkers are evaluated in the order provided.
func NewPermissionChain(checkers ...tools.ToolPermissionChecker) PermissionChain {
	return &chainPermissionChain{checkers: checkers}
}

// Check evaluates all checkers in sequence.
// Returns the first non-allow decision, or allow if all checkers pass.
// If a checker returns updated input, it's passed to subsequent checkers.
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

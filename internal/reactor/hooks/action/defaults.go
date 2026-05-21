package action

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
	"github.com/DotNetAge/goreact/tools"
)

// Defaults returns the default set of tool/action hooks.
func Defaults(askPermission *tools.AskPermission, enforcer *reactor.ToolResultBudgetEnforcer, logger core.Logger) []reactor.ToolHook {
	return []reactor.ToolHook{
		&PermissionHook{Chain: core.NewPermissionChain(askPermission)},
		&ToolEventHook{},
		&ToolLoggerHook{Logger: logger},
		&BudgetHook{Enforcer: enforcer},
	}
}

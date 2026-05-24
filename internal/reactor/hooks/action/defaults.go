package action

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
	"github.com/DotNetAge/goreact/tools"
)

// Defaults returns the default set of tool/action hooks.
// ruleStore and skillRegistry are optional — pass nil to skip the corresponding permission checks.
func Defaults(ruleStore core.PermissionRuleStore, skillRegistry core.SkillRegistry, enforcer *reactor.ToolResultBudgetEnforcer, logger core.Logger) []reactor.ToolHook {
	checkers := []core.ToolPermissionChecker{
		core.NewSkillBasedChecker(skillRegistry),
	}
	if ruleStore != nil {
		checkers = append(checkers, core.NewRuleBasedChecker(ruleStore))
	}
	checkers = append(checkers, tools.NewFallbackPermissionChecker())

	return []reactor.ToolHook{
		&PermissionHook{Chain: core.NewPermissionChain(checkers...), Logger: logger},
		&ToolEventHook{},
		&ToolLoggerHook{Logger: logger},
		&BudgetHook{Enforcer: enforcer},
	}
}

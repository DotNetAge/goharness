package action
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/permission"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

// Defaults returns the default set of tool hooks for the action phase.
// The hooks are returned in priority order and include:
// - PermissionHook: checks tool execution permissions
// - ToolEventHook: emits tool lifecycle events
// - ToolLoggerHook: logs tool execution start/end
func Defaults(ruleStore rule.PermissionRuleStore, skillRegistry skill.SkillRegistry, logger logging.Logger) []hooks.ToolHook {
	checkers := []tools.ToolPermissionChecker{
		permission.NewSkillBasedChecker(skillRegistry),
	}
	if ruleStore != nil {
		checkers = append(checkers, permission.NewRuleBasedChecker(ruleStore))
	}
	checkers = append(checkers, tools.NewFallbackPermissionChecker())

	return []hooks.ToolHook{
		&PermissionHook{Chain: permission.NewPermissionChain(checkers...), Logger: logger},
		&ToolEventHook{},
		&ToolLoggerHook{Logger: logger},
	}
}

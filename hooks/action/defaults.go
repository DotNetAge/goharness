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
// Hooks are returned in priority order (lower = earlier execution).
//
// ToolExecStart and ToolExecEnd events are emitted DIRECTLY by
// Runtime.executeSingleTool(). No event-emission hook is included here.
//
// Registered hooks:
//   - PermissionHook (41): Evaluates tool execution permissions via a 3-level chain:
//                           SkillBasedChecker → RuleBasedChecker → FallbackChecker.
//                           Denies execution and aborts loop on permission failure.
//   - ToolLoggerHook (46): Logs tool execution start/end when Logger is configured.
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
		&ToolLoggerHook{Logger: logger},
	}
}

package action
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/permission"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

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

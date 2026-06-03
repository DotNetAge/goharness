package action

import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/permission"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

// TrackerProvider 根据 sessionID 返回对应的文件修改追踪函数。
// 返回的 bool 表示该 session 是否启用了文件修改追踪。
type TrackerProvider func(sessionID string) (TrackFunc, bool)

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
//   - FileModifyHook (42): Tracks file modifications (Write/FileEdit) by backing up
//                           files before they are modified. Only active when tracker
//                           provider is non-nil.
//   - ToolLoggerHook (46): Logs tool execution start/end when Logger is configured.
func Defaults(ruleStore rule.PermissionRuleStore, skillRegistry skill.SkillRegistry, logger logging.Logger, trackerProvider ...TrackerProvider) []hooks.ToolHook {
	checkers := []tools.ToolPermissionChecker{
		permission.NewSkillBasedChecker(skillRegistry),
	}
	if ruleStore != nil {
		checkers = append(checkers, permission.NewRuleBasedChecker(ruleStore))
	}
	checkers = append(checkers, tools.NewFallbackPermissionChecker())

	result := []hooks.ToolHook{
		&PermissionHook{Chain: permission.NewPermissionChain(checkers...), Logger: logger},
		&ToolLoggerHook{Logger: logger},
	}

	// 可选：当提供了 trackerProvider 时，注册 FileModifyHook
	if len(trackerProvider) > 0 && trackerProvider[0] != nil {
		fmHook := &FileModifyHook{
			Logger:           logger,
			trackerProvider:   trackerProvider[0],
			priority:         PriorityFileModify,
		}
		// 插入到正确位置：Permission(41) < FileModify(42) < Logger(46)
		result = insertHookByPriority(result, fmHook)
	}

	return result
}

// insertHookByPriority 将 hook 按优先级顺序插入到已有列表中。
func insertHookByPriority(list []hooks.ToolHook, newHook hooks.ToolHook) []hooks.ToolHook {
	p := newHook.Priority()
	for i, h := range list {
		if h.Priority() > p {
			result := make([]hooks.ToolHook, 0, len(list)+1)
			result = append(result, list[:i]...)
			result = append(result, newHook)
			result = append(result, list[i:]...)
			return result
		}
	}
	return append(list, newHook)
}

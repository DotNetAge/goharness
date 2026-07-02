package action

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/skill"
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
//   - FileModifyHook (42): Tracks file modifications (Write/FileEdit) by backing up
//     files before they are modified. Only active when tracker
//     provider is non-nil.
//   - ToolLoggerHook (46): Logs tool execution start/end when Logger is configured.
//
// Permission enforcement is no longer a tool hook — it is now an in-tool
// concern (see tools.PermissionRequired). The runtime calls Grant() before
// each tool call; denied tools are stopped at the runtime level and the
// permission flow is invisible to the LLM.
func Defaults(_ /* ruleStore */ interface{}, _ /* skillRegistry */ skill.SkillRegistry, logger logging.Logger, trackerProvider ...TrackerProvider) []hooks.ToolHook {
	// FileModifyHook 始终被注册（provider 可为 nil），
	// 以便后期通过 FileModifyHook.SetProvider() 动态注入 tracker。
	// 见 Runtime.WithFileModifyTracker()。
	var tp TrackerProvider
	if len(trackerProvider) > 0 {
		tp = trackerProvider[0]
	}
	fmHook := &FileModifyHook{
		Logger:          logger,
		trackerProvider: tp,
		priority:        PriorityFileModify,
	}

	result := []hooks.ToolHook{
		fmHook,
		&ToolLoggerHook{Logger: logger},
	}

	return result
}

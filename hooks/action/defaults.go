package action

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/skill"
)

// TrackerProvider 根据 sessionID 返回对应的文件修改追踪函数。
// 返回的 bool 表示该 session 是否启用了文件修改追踪。
type TrackerProvider func(sessionID string) (TrackFunc, bool)

// Defaults 返回 action 阶段的默认工具钩子集合。
// 钩子按优先级顺序返回（数值越小越先执行）。
//
// ToolExecStart 和 ToolExecEnd 事件由 Runtime.executeSingleTool() 直接发射，
// 此处不包含事件发射钩子。
//
// 注册的钩子：
//   - FileModifyHook (42)：通过在文件被修改前备份来追踪文件修改（Write/FileEdit）。
//     仅当 tracker provider 非 nil 时激活。
//   - ToolLoggerHook (46)：当配置了 Logger 时记录工具执行的开始/结束。
//
// 权限强制不再是工具钩子——它现在是工具内部关注点
// （见 tools.PermissionRequired）。运行时在每次工具调用前调用 Grant()；
// 被拒绝的工具在运行时层被阻止，权限流程对 LLM 不可见。
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

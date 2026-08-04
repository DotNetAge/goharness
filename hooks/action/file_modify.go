package action

import (
	"fmt"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// PriorityFileModify 是文件修改追踪钩子的优先级。
// 在权限检查（41）之后、工具日志（46）之前运行。
const PriorityFileModify = 42

// fileModifyingTools 是需要追踪文件修改的工具名称集合。
var fileModifyingTools = map[string]bool{
	"Write":    true,
	"Edit": true,
}

// TrackFunc 是文件修改追踪的函数签名，由 Session.TrackModify 满足。
type TrackFunc func(filePath string) error

// FileModifyHook 是一个 ToolHook 实现，在文件修改类工具（Write、FileEdit）
// 执行前自动调用 TrackFunc 进行备份和追踪。
//
// 通过 TrackerProvider 依赖注入而非直接耦合 Session，使 Hook 可独立测试和复用。
// Runtime 在每次执行时通过 Provider 注入当前 session 的 TrackModify。
//
// 在 Hook 链中的位置：
//
//	PermissionHook(41) → FileModifyHook(42) → ToolLoggerHook(46) → 工具执行
type FileModifyHook struct {
	trackerProvider TrackerProvider
	Logger          logging.Logger
	priority        int
}

// NewFileModifyHook 创建一个文件修改追踪 Hook。
//
// 参数：
//   - provider: 追踪器提供者，根据 sessionID 返回对应的 TrackFunc
//   - logger: 日志记录器（可为 nil）
func NewFileModifyHook(provider TrackerProvider, logger logging.Logger) *FileModifyHook {
	return &FileModifyHook{
		trackerProvider: provider,
		Logger:          logger,
		priority:        PriorityFileModify,
	}
}

func (h *FileModifyHook) Priority() int { return h.priority }

// Before 在工具执行前检查是否为文件修改类工具，
// 如果是则通过 TrackerProvider 获取当前 session 的追踪函数，
// 对目标文件进行备份和追踪。
//
// 该方法不会中止工具执行（即使追踪失败也返回空 HookResult），
// 因为追踪失败不应阻止正常的工具操作。
func (h *FileModifyHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	if h.trackerProvider == nil {
		return hooks.HookResult{}
	}
	if !fileModifyingTools[toolName] {
		return hooks.HookResult{}
	}

	filePath := extractFilePath(params)
	if filePath == "" {
		return hooks.HookResult{}
	}

	tracker, ok := h.trackerProvider(sessionID)
	if !ok || tracker == nil {
		return hooks.HookResult{}
	}

	if err := tracker(filePath); err != nil {
		if h.Logger != nil {
			h.Logger.Error("[file_modify_hook] track failed", err,
				"session", sessionID, "tool", toolName, "path", filePath)
		}
		// 追踪失败不阻止执行，仅记录错误
		return hooks.HookResult{Error: fmt.Errorf("file modify track failed: %w", err)}
	}

	if h.Logger != nil {
		h.Logger.Info("[file_modify_hook] tracked",
			"session", sessionID, "tool", toolName, "path", filePath)
	}

	return hooks.HookResult{}
}

// After 对 FileModifyHook 是空操作。
func (h *FileModifyHook) After(result *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// SetProvider 动态更新 tracker provider，支持后期注入。
// 当 Runtime 在初始化后才设置 fileModifyTracker 时，
// 可以通过此方法直接更新已注册的 FileModifyHook 的 provider。
func (h *FileModifyHook) SetProvider(provider TrackerProvider) {
	h.trackerProvider = provider
}

// Abort 对 FileModifyHook 是空操作。
func (h *FileModifyHook) Abort(reason string) {}

// extractFilePath 从工具参数中提取目标文件路径。
// 兼容多种命名约定：
//   - path / file_path / filepath（蛇形 / 全小写）
//   - filePath（驼峰，Write 工具使用）
func extractFilePath(params map[string]any) string {
	for _, key := range []string{"filePath", "path", "file_path", "filepath"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

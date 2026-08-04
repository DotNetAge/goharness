package loop

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// Defaults 返回 Think-Act 循环的默认循环钩子集合。
// 钩子按优先级顺序返回（数值越小越先执行）。
//
// 所有生命周期事件（CycleEnd、FinalAnswer、ExecutionSummary、LLMTimeout、
// MaxTurnsReached 等）由 Runtime.exec() 直接发射。
// 此处不需要也不包含事件发射钩子。
//
// 注册的钩子：
//   - LoopLoggerHook (45)：当配置了 Logger 时记录 LLM 调用的开始/结束。
//   - ConvergenceHook (49)：检测不可恢复的工具错误（认证失败、权限拒绝等）
//     并中止循环。
func Defaults(logger logging.Logger) []hooks.LoopHook {
	return []hooks.LoopHook{
		&LoopLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

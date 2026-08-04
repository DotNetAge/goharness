package tools

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
)

// SleepTool 实现了 agent 框架的睡眠/等待工具。
// 允许 agent 暂停执行指定时长，适用于并发/异步场景下等待
// 或需要显式休息的情况。
//
// 与 Bash(sleep ...) 不同，此工具不会占用 shell 进程，
// 且支持 context 取消以实现干净的中断。
//
// 安全级别：LevelSafe（无副作用，只读操作）
type SleepTool struct{}

// NewSleepTool 创建一个 SleepTool 实例。
//
// 返回：
//   - FuncTool: 一个 SleepTool 实例
func NewSleepTool() FuncTool {
	return &SleepTool{}
}

// Info 返回 Sleep 工具的元数据。
func (t *SleepTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "Sleep",
		Description: "等待指定的持续时间。用户可以随时中断睡眠。",
		Prompt: `等待指定的持续时间。用户可以随时中断睡眠。

当用户要求你睡眠或休息时，当你无事可做时，或者当你等待某事时，请使用此工具。

你可以与其他工具并发调用此工具，它不会干扰它们。优先使用此工具而不是 Bash(sleep ...)。`,
		Tags:          []string{"sleep", "wait", "delay", "rest"},
		SecurityLevel: events.LevelSafe,
		IsIdempotent:  true,
		IsReadOnly:    true,
		Parameters: []Parameter{
			{
				Name:        "duration_ms",
				Type:        "integer",
				Description: "等待多长时间（毫秒）。默认为 5000（5 秒）。最小值为 1000（1 秒）。最大值为 300000（5 分钟）。",
				Required:    false,
			},
		},
	}
}

// Execute 执行睡眠操作。
//
// 参数：
//   - ctx: 上下文（支持取消以实现中断）
//   - params: 可选 "duration_ms"（默认 5000，最小 1000，最大 300000）
//
// 返回：
//   - map[string]any: 包含 "slept_ms"（实际睡眠时长）和 "status"（"completed" 或 "interrupted"）
//   - error: 仅在参数校验失败时返回
func (t *SleepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	durationMs := 5000
	if raw, found := GetParam(params, "duration_ms"); found {
		if v, ok := ToFloat64(raw); ok && v > 0 {
			durationMs = int(v)
			if durationMs < 1000 {
				durationMs = 1000
			}
			if durationMs > 300000 {
				durationMs = 300000
			}
		}
	}

	logger := getLogger(ctx)
	sessionID := ExtractSessionID(ctx)

	logger.Info("sleep tool invoked",
		"duration_ms", durationMs,
		"session_id", sessionID,
	)

	duration := time.Duration(durationMs) * time.Millisecond
	startTime := time.Now()

	select {
	case <-ctx.Done():
		elapsed := time.Since(startTime)
		logger.Info("sleep interrupted",
			"elapsed_ms", elapsed.Milliseconds(),
			"session_id", sessionID,
		)
		return map[string]any{
			"slept_ms":  elapsed.Milliseconds(),
			"status":    "interrupted",
			"remaining": duration.Milliseconds() - elapsed.Milliseconds(),
		}, nil
	case <-time.After(duration):
		elapsed := time.Since(startTime)
		logger.Info("sleep completed",
			"elapsed_ms", elapsed.Milliseconds(),
			"session_id", sessionID,
		)
		return map[string]any{
			"slept_ms": elapsed.Milliseconds(),
			"status":   "completed",
		}, nil
	}
}

// 确保在编译期 SleepTool 实现 FuncTool 接口。
var _ FuncTool = (*SleepTool)(nil)

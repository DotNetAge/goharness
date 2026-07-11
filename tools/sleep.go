package tools

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
)

// SleepTool implements a sleep/wait tool for the agent framework.
// Allows agents to pause execution for a specified duration, useful for
// waiting in concurrent/async scenarios or when explicit rest is needed.
//
// Unlike Bash(sleep ...), this tool does not hold a shell process,
// and respects context cancellation for clean interruption.
//
// Security level: LevelSafe (no side effects, read-only operation)
type SleepTool struct{}

// NewSleepTool creates a SleepTool instance.
//
// Returns:
//   - FuncTool: a SleepTool instance
func NewSleepTool() FuncTool {
	return &SleepTool{}
}

// Info returns the Sleep tool's metadata.
func (t *SleepTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "Sleep",
		Description: "等待指定的持续时间。用户可以随时中断睡眠。",
		Prompt: `等待指定的持续时间。用户可以随时中断睡眠。

当用户要求你睡眠或休息时，当你无事可做时，或者当你等待某事时，请使用此工具。

你可能会收到 <tick> 提示——这些是定期检查。在睡眠前寻找有用的工作要做。

你可以与其他工具并发调用此工具——它不会干扰它们。

优先使用此工具而不是 Bash(sleep ...)——它不会持有 shell 进程。

每次唤醒都会消耗一次 API 调用，但提示缓存在 5 分钟不活动后过期——相应地平衡。`,
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

// Execute performs the sleep operation.
//
// Parameters:
//   - ctx: context (supports cancellation for interruption)
//   - params: optional "duration_ms" (default 5000, minimum 1000, maximum 300000)
//
// Returns:
//   - map[string]any: contains "slept_ms" (actual duration slept) and "status" ("completed" or "interrupted")
//   - error: only if parameter validation fails
func (t *SleepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	durationMs := 5000
	if raw, ok := params["duration_ms"]; ok {
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

// Ensure SleepTool implements FuncTool at compile time.
var _ FuncTool = (*SleepTool)(nil)

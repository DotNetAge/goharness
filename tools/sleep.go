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
		Description: "Wait for a specified duration. The user can interrupt the sleep at any time.",
		Prompt: `Wait for a specified duration. The user can interrupt the sleep at any time.

Use this when the user tells you to sleep or rest, when you have nothing to do, or when you're waiting for something.

You may receive <tick> prompts — these are periodic check-ins. Look for useful work to do before sleeping.

You can call this concurrently with other tools — it won't interfere with them.

Prefer this over Bash(sleep ...) — it doesn't hold a shell process.

Each wake-up costs an API call, but the prompt cache expires after 5 minutes of inactivity — balance accordingly.`,
		Tags:          []string{"sleep", "wait", "delay", "rest"},
		SecurityLevel: events.LevelSafe,
		IsIdempotent:  true,
		IsReadOnly:    true,
		Parameters: []Parameter{
			{
				Name:        "duration_ms",
				Type:        "integer",
				Description: "How long to wait in milliseconds. Default is 5000 (5 seconds). Minimum is 1000 (1 second). Maximum is 300000 (5 minutes).",
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

package loop

import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
)

// Defaults returns the default set of loop hooks for the Think-Act loop.
// Hooks are returned in priority order (lower = earlier execution).
//
// All lifecycle events (CycleEnd, FinalAnswer, ExecutionSummary, LLMTimeout,
// MaxTurnsReached, etc.) are emitted DIRECTLY by Runtime.exec().
// No event-emission hook is needed or included here.
//
// Registered hooks:
//   - LoopLoggerHook (45): Logs LLM call start/end when Logger is configured.
//   - ConvergenceHook (49): Detects irrecoverable tool errors (auth failures,
//                           permission denied, etc.) and aborts the loop.
func Defaults(logger logging.Logger) []hooks.LoopHook {
	return []hooks.LoopHook{
		&LoopLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

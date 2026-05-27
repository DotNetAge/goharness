package loop
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
)

// Defaults returns the default set of loop hooks for the Think-Act loop.
// The hooks are returned in priority order and include:
// - PreCheckHook: checks termination conditions
// - LoopEventHook: emits loop lifecycle events
// - LoopLoggerHook: logs loop start/end events
// - ConvergenceHook: checks for irrecoverable errors
func Defaults(logger logging.Logger) []hooks.LoopHook {
	return []hooks.LoopHook{
		&PreCheckHook{},
		&LoopEventHook{},
		&LoopLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

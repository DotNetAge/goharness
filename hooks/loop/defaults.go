package loop
import (
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
)

// Defaults returns the default set of loop hooks.
func Defaults(logger logging.Logger) []hooks.LoopHook {
	return []hooks.LoopHook{
		&PreCheckHook{},
		&LoopEventHook{},
		&LoopLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

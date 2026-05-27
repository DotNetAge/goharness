package loop

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// Defaults returns the default set of loop hooks.
func Defaults(logger core.Logger) []reactor.LoopHook {
	return []reactor.LoopHook{
		&PreCheckHook{},
		&LoopEventHook{},
		&LoopLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

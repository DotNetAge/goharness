package thought

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// Defaults returns the default set of thought hooks.
func Defaults(logger core.Logger) []reactor.ThoughtHook {
	return []reactor.ThoughtHook{
		&PreCheckHook{},
		&ThoughtEventHook{},
		&ThoughtLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

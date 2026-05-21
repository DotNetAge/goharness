package observation

import (
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/reactor"
)

// Defaults returns the default set of observation hooks.
func Defaults(logger core.Logger) []reactor.ObservationHook {
	return []reactor.ObservationHook{
		&ObservationEventHook{},
		&ObservationLoggerHook{Logger: logger},
		&ConvergenceHook{},
	}
}

package events

import "time"

type CycleInfo struct {
	Iteration         int           `json:"iteration"`
	TerminationReason string        `json:"termination_reason,omitempty"`
	Duration          time.Duration `json:"duration_ns"`
}

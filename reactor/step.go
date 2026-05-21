package reactor

import "time"

// Step represents one complete Think-Act-Observe (T-A-O) cycle iteration.
// It aggregates the outputs of all three phases into a single record for
// history tracking, debugging, and event emission.
type Step struct {
	Iteration int `json:"iteration" yaml:"iteration"`
	// Iteration is the 1-based index of this cycle within the run.

	Thought Thought `json:"thought" yaml:"thought"`
	// Thought holds the reasoning output from the Think phase.

	Action Action `json:"action" yaml:"action"`
	// Action holds the execution output from the Act phase.

	Observation Observation `json:"observation" yaml:"observation"`
	// Observation holds the result feedback from the Observe phase.

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`
	// Timestamp records when this step was completed.

	Duration time.Duration `json:"duration" yaml:"duration"`
	// Duration measures the wall-clock time taken for the entire T-A-O cycle.
}

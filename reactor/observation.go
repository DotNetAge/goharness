package reactor

import "time"

// Observation represents the output of the Observe phase in the T-A-O cycle.
// It captures the result of executing an Action, including success/failure status,
// the raw result data, any derived insights, and error information for retry logic.
type Observation struct {
	Success bool `json:"success" yaml:"success"`
	// Success indicates whether the action completed without error.

	Result string `json:"result" yaml:"result"`
	// Result contains the output or return value from the executed action.

	Insights []string `json:"insights,omitempty" yaml:"insights,omitempty"`
	// Insights holds optional analysis or key takeaways derived from the result.

	ShouldRetry bool `json:"should_retry" yaml:"should_retry"`
	// ShouldRetry suggests whether the action should be retried on failure.

	Error string `json:"error,omitempty" yaml:"error,omitempty"`
	// Error contains the error message if the action failed.

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`
	// Timestamp records when this observation was created.
}

// NewSuccessObservation creates a new Observation representing a successful action execution.
//
// Parameters:
//   - result: the output string from the successful action.
//   - insights: zero or more insight strings derived from the result.
//
// Returns a pointer to the newly created Observation with Success=true.
func NewSuccessObservation(result string, insights ...string) *Observation {
	return &Observation{
		Success:   true,
		Result:    result,
		Insights:  insights,
		Timestamp: time.Now(),
	}
}

// NewErrorObservation creates a new Observation representing a failed action execution.
//
// Parameters:
//   - err: the error that caused the failure (may be nil).
//   - shouldRetry: whether the caller should attempt to retry the action.
//
// Returns a pointer to the newly created Observation with Success=false.
func NewErrorObservation(err error, shouldRetry bool) *Observation {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	return &Observation{
		Success:     false,
		Error:       errMsg,
		ShouldRetry: shouldRetry,
		Timestamp:   time.Now(),
	}
}

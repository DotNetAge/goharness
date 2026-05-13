package reactor

import (
	"time"
)

// ActionType identifies the kind of action taken.
type ActionType string

// ActionType constants.
const (
	ActionTypeToolCall ActionType = "tool_call"
	ActionTypeAnswer   ActionType = "answer"
	ActionTypeClarify  ActionType = "clarify"
	ActionTypeDelegate ActionType = "delegate"
)

// Action represents the output of the Act phase.
type Action struct {
	Type      ActionType     `json:"type" yaml:"type"`
	Target    string         `json:"target" yaml:"target"`                   // Tool name (for tool_call)
	Params    map[string]any `json:"params" yaml:"params"`                   // Tool parameters
	Result    string         `json:"result" yaml:"result"`                   // Execution result
	Error     error          `json:"-" yaml:"-"`                             // Execution error (not serialized)
	ErrorMsg  string         `json:"error,omitempty" yaml:"error,omitempty"` // Serialized error message
	Duration  time.Duration  `json:"duration" yaml:"duration"`               // Execution duration
	Timestamp time.Time      `json:"timestamp" yaml:"timestamp"`             // When the action was taken
}

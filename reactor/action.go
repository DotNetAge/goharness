package reactor

import (
	"fmt"
	"strings"
	"time"
)

// ToolResult holds the execution result of a single tool call.
type ToolResult struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ms"`
	Success    bool          `json:"success"`
}

// ToolResultSummary builds the one-line summary string for a single tool result.
func (t ToolResult) ToolResultSummary() string {
	if t.Error != "" {
		return fmt.Sprintf("[%s] error: %s", t.ToolName, t.Error)
	}
	return fmt.Sprintf("[%s] %s", t.ToolName, t.Result)
}

// Action represents the output of the Act phase.
// An Action maps to N tool calls (N >= 0). Per-tool results are in Results.
//
// The old ActionType field (ActionTypeToolCall, ActionTypeAnswer, etc.) has been removed.
// Action classification is now inferred from Thought.Decision, which is stored
// alongside Action in Step. When only Action is available, Results content
// (ToolName, Success) determines semantics.
type Action struct {
	Results   []ToolResult   `json:"results" yaml:"results"`                 // Per-tool execution results (N >= 0)
	Duration  time.Duration  `json:"duration" yaml:"duration"`               // Execution duration
	Timestamp time.Time      `json:"timestamp" yaml:"timestamp"`             // When the action was taken
}

// Summary joins per-tool results into a single string (for events, observations, etc.)
func (a *Action) Summary() string {
	var parts []string
	for _, r := range a.Results {
		parts = append(parts, r.ToolResultSummary())
	}
	return strings.Join(parts, "\n")
}

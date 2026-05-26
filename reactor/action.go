package reactor

import (
	"fmt"
	"strings"
	"time"
)

// ToolResult holds the execution result of a single tool call within an Action.
// It captures the outcome, output, error (if any), and timing for each
// individual tool invocation.
type ToolResult struct {
	ToolName string `json:"tool_name"`
	// ToolName is the registered name of the executed tool.

	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCallID is the original identifier from the LLM's function call response.
	// Used to correlate results back to the LLM's conversation context.

	Result string `json:"result,omitempty"`
	// Result contains the stringified output intended for the LLM.

	Metadata any `json:"metadata,omitempty"`
	// Metadata carries structured data for system consumers (UI, hooks, logging).
	// Not sent to the LLM. Populated from ToolExecutionResult.Metadata.

	Error string `json:"error,omitempty"`
	// Error contains the error message if the tool execution failed.

	Duration time.Duration `json:"duration_ms"`
	// Duration records how long the tool took to execute.

	Success bool `json:"success"`
	// Success indicates whether the tool completed without error.
}

// ToolResultSummary returns a single-line human-readable summary of this tool result.
//
// On success, it returns "[ToolName] result_content".
// On failure, it returns "[ToolName] error: error_message".
func (t ToolResult) ToolResultSummary() string {
	if t.Error != "" {
		return fmt.Sprintf("[%s] error: %s", t.ToolName, t.Error)
	}
	return fmt.Sprintf("[%s] %s", t.ToolName, t.Result)
}

// Action represents the output of the Act phase in the Think-Act cycle.
// An Action maps to N tool calls (N >= 0), with per-tool results stored in Results.
//
// The old ActionType field (ActionTypeToolCall, ActionTypeAnswer, etc.) has been removed.
// Action classification is now inferred from Thought.Decision, which is stored
// alongside Action in Step. When only Action is available, Results content
// (ToolName, Success) determines semantics.
type Action struct {
	Results []ToolResult `json:"results" yaml:"results"`
	// Results holds per-tool execution outcomes (N >= 0). Empty means no tools were called.

	Duration time.Duration `json:"duration" yaml:"duration"`
	// Duration measures the total wall-clock time for executing all tool calls.

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`
	// Timestamp records when this action was taken.
}

// Summary joins all per-tool result summaries into a single newline-separated string.
// This is used for event emission and logging.
func (a *Action) Summary() string {
	var parts []string
	for _, r := range a.Results {
		parts = append(parts, r.ToolResultSummary())
	}
	return strings.Join(parts, "\n")
}

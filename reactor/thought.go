package reactor

import (
	"encoding/json"
	"time"
)

// Decision constants define the possible values for Thought.Decision.
// These are derived from the native LLM response, not a custom JSON format.
const (
	DecisionAct    = "act"    // LLM called tools (native ToolCalls present)
	DecisionAnswer = "answer" // LLM answered directly (no ToolCalls)
)

// ToolCallItem represents a single tool call with its name, parameters, and ID.
// Used in Thought.ToolCallList to preserve ordering and support duplicate tool names
// across parallel calls (unlike ToolCalls map which deduplicates by key).
type ToolCallItem struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	ID        string         `json:"id,omitempty"`
}

// Thought is the event payload derived from the native LLM response.
// It is NOT a format the LLM is instructed to output — instead, it wraps
// the native ToolCalls, FinishReason, and Content into a structured form
// for event emission, logging, and downstream consumers.
type Thought struct {
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
	// Content is the raw verbatim text output from the LLM (streaming content).

	Reasoning string `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	// Reasoning contains the LLM's step-by-step reasoning (native thinking content).

	Decision string `json:"decision" yaml:"decision"`
	// Decision indicates the action: "act" (LLM called tools) or "answer" (LLM answered).
	// Derived from the presence of native ToolCalls.

	FinishReason string `json:"finish_reason,omitempty" yaml:"finish_reason,omitempty"`
	// FinishReason is the native stop_reason from the LLM API (e.g., "stop", "tool_calls", "length").

	ToolCalls map[string]map[string]any `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	// ToolCalls holds multiple tool calls for batch parallel execution.
	// Map key = tool name, value = parameter map.
	// NOTE: map key deduplicates same-named parallel calls. Prefer ToolCallList
	// for execution — ToolCalls is retained for JSON backward compatibility.

	ToolCallList []ToolCallItem `json:"tool_call_list,omitempty" yaml:"tool_call_list,omitempty"`
	// ToolCallList preserves the original tool call ordering and supports
	// parallel calls with the same tool name (no key deduplication).

	ToolCallIDs map[string]string `json:"tool_call_ids,omitempty" yaml:"tool_call_ids,omitempty"`
	// ToolCallIDs maps tool name → original tool_call_id from the LLM response.

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`
	// Timestamp records when this thought was produced.
}

// ToMap converts Thought to a map[string]any for event bus transmission.
// This allows consumers (like MindX TUI) to access Thought fields without
// importing the reactor package.
func (t *Thought) ToMap() map[string]any {
	if t == nil {
		return nil
	}
	data, _ := json.Marshal(t)
	var result map[string]any
	json.Unmarshal(data, &result)
	return result
}

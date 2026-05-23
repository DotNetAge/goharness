package reactor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// Decision constants define the possible values for Thought.Decision.
// These determine the next phase behavior in the T-A-O cycle.
const (
	DecisionAct      = "act"       // Execute tool calls
	DecisionAnswer   = "answer"    // Return a final answer to the user
	DecisionClarify  = "clarify"   // Ask the user a clarification question
	DecisionSubAgent = "subagent"  // Delegate to a sub-agent
)

// ToolCallItem represents a single tool call with its name, parameters, and ID.
// Used in Thought.ToolCallList to preserve ordering and support duplicate tool names
// across parallel calls (unlike ToolCalls map which deduplicates by key).
type ToolCallItem struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	ID        string         `json:"id,omitempty"`
}

// Thought represents the output of the Think (reasoning) phase in the T-A-O cycle.
// It encapsulates the LLM's reasoning process, decision, and any associated data
// needed for the subsequent Act or Answer phases.
type Thought struct {
	Reasoning string `json:"reasoning" yaml:"reasoning"`
	// Reasoning contains the LLM's step-by-step reasoning explanation.

	Decision string `json:"decision" yaml:"decision"`
	// Decision indicates the chosen action: "act", "answer", "clarify", or "subagent".

	Confidence float64 `json:"confidence" yaml:"confidence"`
	// Confidence is the LLM's confidence level in its decision (0.0–1.0).

	IsFinal bool `json:"is_final" yaml:"is_final"`
	// IsFinal marks whether this thought concludes the reasoning loop.

	FinalAnswer string `json:"final_answer,omitempty" yaml:"final_answer"`
	// FinalAnswer holds the response content when Decision is DecisionAnswer.

	ToolCalls map[string]map[string]any `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	// ToolCalls holds multiple tool calls for batch parallel execution (v2).
	// Map key = tool name, value = parameter map.
	// NOTE: map key deduplicates same-named parallel calls. Prefer ToolCallList
	// for execution — ToolCalls is retained for JSON backward compatibility.

	ToolCallList []ToolCallItem `json:"tool_call_list,omitempty" yaml:"tool_call_list,omitempty"`
	// ToolCallList preserves the original tool call ordering and supports
	// parallel calls with the same tool name (no key deduplication).
	// Populated by nativeToolCallsToThought; preferred by parseToolCalls
	// and ToolEventHook over the ToolCalls map.

	ClarificationQuestion string `json:"clarification_question,omitempty" yaml:"clarification_question"`
	// ClarificationQuestion stores the question to ask the user when Decision is DecisionClarify.

	SubAgentTarget string `json:"subagent_target,omitempty" yaml:"subagent_target"`
	// SubAgentTarget identifies the sub-agent or service to delegate to.

	SubAgentPrompt string `json:"subagent_prompt,omitempty" yaml:"subagent_prompt"`
	// SubAgentPrompt contains the task description forwarded to the sub-agent target.

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`
	// Timestamp records when this thought was produced.

	ToolCallIDs map[string]string `json:"tool_call_ids,omitempty" yaml:"tool_call_ids,omitempty"`
	// ToolCallIDs maps tool name → original tool_call_id from the LLM response.
	// Populated by nativeToolCallsToThought; used when persisting tool results.
	// NOTE: same-name parallel calls only retain the last ID in this map.
	// Prefer ToolCallList[].ID for per-call ID resolution.
}

// jsonBlockRegex matches ```json ... ``` code blocks.
var jsonBlockRegex = regexp.MustCompile("(?s)```(?:json)?\\s*\n?(.*?)\n?\\s*```")

// stripJSONWrappers removes markdown code fences and leading/trailing whitespace from LLM output.
func stripJSONWrappers(s string) string {
	s = strings.TrimSpace(s)
	if m := jsonBlockRegex.FindStringSubmatch(s); len(m) > 1 {
		s = strings.TrimSpace(m[1])
	}
	return s
}

// ParseThinkResponse parses an LLM response string into a Thought struct.
// If the content is not valid JSON (e.g., LLM returned a direct text answer),
// it will be automatically wrapped as a DecisionAnswer Thought.
//
// The logger parameter enables dependency injection for TUI/testing environments.
// Pass nil to disable logging for this operation.
func ParseThinkResponse(content string, logger core.Logger) (*Thought, error) {
	content = stripJSONWrappers(content)

	var thought Thought
	if err := json.Unmarshal([]byte(content), &thought); err != nil {
		// Check if content looks like a direct answer (non-empty, substantial text)
		trimmed := strings.TrimSpace(content)
		if len(trimmed) > 10 && looksLikeDirectAnswer(trimmed) {
			if logger != nil {
				logger.Info("parsing non-JSON response as direct answer",
					"content_length", len(trimmed),
					"preview", Truncate(trimmed, 80),
				)
			}
			return &Thought{
				Decision:    DecisionAnswer,
				Reasoning:   "LLM returned direct text answer (not JSON)",
				FinalAnswer: trimmed,
				IsFinal:     true,
				Timestamp:   time.Now(),
			}, nil
		}
		return nil, fmt.Errorf("failed to parse thought JSON: %w\nraw: %s", err, Truncate(content, 200))
	}

	// Normalize decision
	thought.Decision = strings.ToLower(strings.TrimSpace(thought.Decision))
	switch thought.Decision {
	case DecisionAct, DecisionAnswer, DecisionClarify, DecisionSubAgent:
		// valid
	default:
		thought.Decision = DecisionAnswer
	}

	if thought.Timestamp.IsZero() {
		thought.Timestamp = time.Now()
	}

	return &thought, nil
}

// looksLikeDirectAnswer checks if the content appears to be a direct text answer
// rather than malformed JSON or an error message.
// It uses heuristics: length > 10 chars, contains natural language patterns,
// doesn't look like JSON or code.
func looksLikeDirectAnswer(content string) bool {
	// Must have substantial content
	if len(content) <= 10 {
		return false
	}

	// Should not start with JSON-like patterns
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}

	// Should not be just whitespace or special characters
	hasLetter := false
	for _, r := range content {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 0x4e00 && r <= 0x9fff) {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return false
	}

	// Should contain common answer patterns (optional heuristic)
	answerPatterns := []string{
		"根据", "以下是", "总结", "回答", "结果", "结论",
		"based on", "here is", "in summary", "the answer", "result",
		"## ", "### ", "**", "* ",
	}
	for _, pattern := range answerPatterns {
		if strings.Contains(strings.ToLower(content), strings.ToLower(pattern)) {
			return true
		}
	}

	// If content is long enough (>50 chars) and has letters, likely an answer
	return len(content) > 50
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

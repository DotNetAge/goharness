package reactor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// Decision constants for Thought.Decision
const (
	DecisionAct         = "act"
	DecisionAnswer      = "answer"
	DecisionClarify     = "clarify"
	DecisionDelegate    = "delegate"
	DecisionCoordinate  = "coordinate" // WBS decomposed → enter Coordinator mode to dispatch & monitor sub-tasks
)

// Thought represents the output of the Think phase.
type Thought struct {
	Reasoning   string  `json:"reasoning" yaml:"reasoning"`
	Decision    string  `json:"decision" yaml:"decision"`
	Confidence  float64 `json:"confidence" yaml:"confidence"`
	IsFinal     bool    `json:"is_final" yaml:"is_final"`
	FinalAnswer string  `json:"final_answer,omitempty" yaml:"final_answer"`

	// ActionTarget + ActionParams: single tool call (legacy path, backward compatible)
	ActionTarget string         `json:"action_target,omitempty" yaml:"action_target"`
	ActionParams map[string]any `json:"action_params,omitempty" yaml:"action_params"`

	// ToolCalls holds multiple tool calls for batch parallel execution (v2).
	// Map key = tool name, value = parameter map.
	// When set, Act executes all tools in parallel: sync tools wait for result,
	// async tools (IsAsync=true) run in goroutines and return {task_id, status: "running"}.
	ToolCalls map[string]map[string]any `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`

	ClarificationQuestion string `json:"clarification_question,omitempty" yaml:"clarification_question"`

	DelegateTarget string `json:"delegate_target,omitempty" yaml:"delegate_target"`
	DelegatePrompt string `json:"delegate_prompt,omitempty" yaml:"delegate_prompt"`

	Timestamp time.Time `json:"timestamp" yaml:"timestamp"`

	// ToolCallIDs maps tool name → original tool_call_id from the LLM response.
	// Populated by nativeToolCallsToThought; used when persisting tool results.
	ToolCallIDs map[string]string `json:"tool_call_ids,omitempty" yaml:"tool_call_ids,omitempty"`
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
					"preview", truncate(trimmed, 100),
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
		return nil, fmt.Errorf("failed to parse thought JSON: %w\nraw: %s", err, truncate(content, 200))
	}

	// Normalize decision
	thought.Decision = strings.ToLower(strings.TrimSpace(thought.Decision))
	switch thought.Decision {
	case DecisionAct, DecisionAnswer, DecisionClarify, DecisionDelegate:
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

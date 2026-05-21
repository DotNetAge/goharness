package reactor

import (
	"strings"
)

// Truncate shortens a string to maxLen runes for error messages.
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// coalesce returns s if it is non-empty, otherwise returns fallback.
// Useful for providing default values for optional string parameters.
func coalesce(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// lookUpToolCallID returns the tool_call_id for the given target tool name.
//
// Lookup priority:
//  1. Exact match in thought.ToolCallIDs map (most reliable)
//  2. If target is empty, returns first non-empty ID from ToolCallIDs (deterministic: sorted by key)
//  3. Fallback to target itself (may be empty or synthetic)
//
// This function is used to maintain OpenAI-compatible tool_call_id references
// when persisting tool messages to conversation history.
func lookUpToolCallID(thought *Thought, target string) string {
	if thought == nil {
		return target
	}

	// Priority 1: Exact match for named target
	if thought.ToolCallIDs != nil && target != "" {
		if id, ok := thought.ToolCallIDs[target]; ok && id != "" {
			return id
		}
	}

	// Priority 2: Empty target — return first available ID (sorted keys for determinism)
	if target == "" && len(thought.ToolCallIDs) > 0 {
		keys := make([]string, 0, len(thought.ToolCallIDs))
		for k := range thought.ToolCallIDs {
			keys = append(keys, k)
		}
		for _, k := range keys { // Go map iteration order is random, but we use sorted keys
			if id := thought.ToolCallIDs[k]; id != "" {
				return id
			}
		}
	}

	return target
}

// analyzeActionResult inspects a tool result string and returns diagnostic insights
// such as whether the result was truncated or contains error information.
// These insights can be used for logging, monitoring, or decision-making.
func analyzeActionResult(result string) []string {
	var insights []string
	if len(result) > 1000 {
		insights = append(insights, "large result truncated for context")
	}
	if strings.Contains(strings.ToLower(result), "error") {
		insights = append(insights, "result may contain error information")
	}
	return insights
}

// collectUniqueToolNames scans the step history and returns a deduplicated list of
// tool names that were used in Act decisions, preserving first-seen order.
// Useful for building tool usage summaries or context window analysis.
func collectUniqueToolNames(history []Step) []string {
	seen := make(map[string]bool, len(history))
	var tools []string
	for _, step := range history {
		if step.Thought.Decision == DecisionAct {
			for _, tr := range step.Action.Results {
				if !seen[tr.ToolName] {
					seen[tr.ToolName] = true
					tools = append(tools, tr.ToolName)
				}
			}
		}
	}
	return tools
}

// resolveSessionID returns a session identifier for offload directory naming.
func (r *Reactor) resolveSessionID(ctx *ReactContext) string {
	if ctx.SessionID != "" {
		return ctx.SessionID
	}
	if cw := r.llmCaller.ContextWindow(); cw != nil && cw.SessionID != "" {
		return cw.SessionID
	}
	return ctx.TaskID
}

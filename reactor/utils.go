package reactor

import (
	"strings"
)

// truncate shortens a string to maxLen runes for error messages.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func coalesce(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// lookUpToolCallID returns the tool_call_id for the given target tool name.
// Falls back to a synthetic ID based on the target name when the original ID
// is not available (e.g., legacy Thought or parsed JSON path).
func lookUpToolCallID(thought *Thought, target string) string {
	if thought == nil {
		return target
	}
	if thought.ToolCallIDs != nil && target != "" {
		if id, ok := thought.ToolCallIDs[target]; ok && id != "" {
			return id
		}
	}
	if target == "" && len(thought.ToolCallIDs) > 0 {
		for _, id := range thought.ToolCallIDs {
			return id
		}
	}
	return target
}

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

func collectUniqueToolNames(history []Step) []string {
	seen := make(map[string]bool, len(history))
	var tools []string
	for _, step := range history {
		if step.Action.Type == ActionTypeToolCall && step.Action.Target != "" {
			if !seen[step.Action.Target] {
				seen[step.Action.Target] = true
				tools = append(tools, step.Action.Target)
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

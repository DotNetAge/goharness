package reactor

// Truncate shortens a string to maxLen runes for error messages.
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// lookUpToolCallID returns the tool_call_id for the given target tool name.
//
// Lookup priority:
//  1. ToolCallList — first matching item by name (supports parallel same-name calls)
//  2. ToolCallIDs map — direct name→ID lookup
//  3. Empty target — first non-empty ID from ToolCallList or ToolCallIDs
//  4. Fallback to target itself (may be empty or synthetic)
//
// This function is used to maintain OpenAI-compatible tool_call_id references
// when persisting tool messages to conversation history.
func lookUpToolCallID(thought *Thought, target string) string {
	if thought == nil {
		return target
	}

	// Priority 1: ToolCallList (ordered, supports same-name parallel calls)
	if len(thought.ToolCallList) > 0 && target != "" {
		for _, item := range thought.ToolCallList {
			if item.Name == target && item.ID != "" {
				return item.ID
			}
		}
	}

	// Priority 2: Exact match in thought.ToolCallIDs map
	if thought.ToolCallIDs != nil && target != "" {
		if id, ok := thought.ToolCallIDs[target]; ok && id != "" {
			return id
		}
	}

	// Priority 3: Empty target — return first available ID
	if target == "" {
		if len(thought.ToolCallList) > 0 {
			for _, item := range thought.ToolCallList {
				if item.ID != "" {
					return item.ID
				}
			}
		}
		if len(thought.ToolCallIDs) > 0 {
			keys := make([]string, 0, len(thought.ToolCallIDs))
			for k := range thought.ToolCallIDs {
				keys = append(keys, k)
			}
			for _, k := range keys {
				if id := thought.ToolCallIDs[k]; id != "" {
					return id
				}
			}
		}
	}

	return target
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

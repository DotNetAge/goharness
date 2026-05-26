package reactor

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// maxDestructiveLoopCount is the number of consecutive identical failed tool calls
	// required to classify the agent as being in a destructive loop.
	maxDestructiveLoopCount = 3
	// maxStuckCount is the number of consecutive non-act decisions (e.g., Observe, Wait)
	// required to classify the agent as stuck.
	maxStuckCount = 4
)

// IsDestructiveLoop detects whether the agent is repeating the same failing tool call
// in a loop without making progress. A destructive loop is identified when:
//   - The last maxDestructiveLoopCount steps are all DecisionAct steps.
//   - They all invoke the same tool (same signature).
//   - They all produce the same error.
//
// This typically indicates the agent is retrying a fundamentally broken approach.
func IsDestructiveLoop(history []Step) bool {
	if len(history) < maxDestructiveLoopCount {
		return false
	}
	tail := history[len(history)-maxDestructiveLoopCount:]
	if len(tail[0].Action.Results) == 0 {
		return false
	}
	firstErr := tail[0].Action.Results[0].Error
	if firstErr == "" {
		return false
	}
	for _, step := range tail[1:] {
		if step.Thought.Decision != DecisionAct {
			return false
		}
		if len(step.Action.Results) == 0 || toolSignature(step) != toolSignature(tail[0]) {
			return false
		}
		if step.Action.Results[0].Error != firstErr {
			return false
		}
	}
	return true
}

// IsAgentStuck detects whether the agent is making no forward progress by repeatedly
// choosing non-Act decisions (e.g., Observe, Wait, Finish) without taking action.
// This can indicate the agent is confused or waiting for conditions that won't change.
func IsAgentStuck(history []Step) bool {
	if len(history) < maxStuckCount {
		return false
	}
	count := 0
	for i := len(history) - 1; i >= 0 && i >= len(history)-maxStuckCount; i-- {
		if history[i].Thought.Decision != DecisionAct {
			count++
		} else {
			break
		}
	}
	return count >= maxStuckCount
}

// IsResultConverged detects whether the agent's last 3 actions all produced identical
// summaries, indicating the agent is generating the same output repeatedly without
// incorporating new information or changing strategy.
func IsResultConverged(history []Step) bool {
	if len(history) < 3 {
		return false
	}
	last3 := history[len(history)-3:]
	if last3[0].Action.Summary() == "" || last3[1].Action.Summary() == "" || last3[2].Action.Summary() == "" {
		return false
	}
	return last3[0].Action.Summary() == last3[1].Action.Summary() && last3[1].Action.Summary() == last3[2].Action.Summary()
}

// IsDuplicateAction detects whether the agent's last two actions are identical
// (same tool signature and same result summary). This is a weaker signal than
// IsDestructiveLoop (only 2 steps) and catches near-term repetition.
func IsDuplicateAction(history []Step) bool {
	if len(history) < 2 {
		return false
	}
	last := history[len(history)-1]
	prev := history[len(history)-2]
	if last.Thought.Decision != DecisionAct || prev.Thought.Decision != DecisionAct {
		return false
	}
	if toolSignature(last) != toolSignature(prev) {
		return false
	}
	if last.Action.Summary() != prev.Action.Summary() {
		return false
	}
	return true
}

// toolSignature builds a stable signature for a Step based on its tool set + params.
// Signature format: "[tool1:params1 tool2:params2 ...]" with tools sorted by name.
func toolSignature(step Step) string {
	var names []string
	for _, tr := range step.Action.Results {
		names = append(names, tr.ToolName)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		paramStr := ""
		// Prefer ToolCallList for parameter lookup (ordered)
		if len(step.Thought.ToolCallList) > 0 {
			for _, item := range step.Thought.ToolCallList {
				if item.Name == name {
					paramStr = fmt.Sprintf("%v", item.Arguments)
					break
				}
			}
		} else if step.Thought.ToolCalls != nil {
			if params, ok := step.Thought.ToolCalls[name]; ok {
				paramStr = fmt.Sprintf("%v", params)
			}
		}
		parts = append(parts, name+":"+paramStr)
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, " "))
}

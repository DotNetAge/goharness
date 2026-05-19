package reactor

import (
	"fmt"
	"sort"
	"strings"
)

// CheckTermination evaluates whether the T-A-O loop should stop.
func (r *Reactor) CheckTermination(ctx *ReactContext) (bool, string) {
	if ctx.CurrentIteration >= ctx.MaxIterations {
		r.getLogger().Debug("termination: max iterations reached",
			"iteration", ctx.CurrentIteration,
			"max", ctx.MaxIterations)
		return true, "reached max iterations"
	}

	if ctx.Ctx().Err() != nil {
		r.getLogger().Debug("termination: context cancelled",
			"error", ctx.Ctx().Err())
		return true, "request cancelled"
	}

	if ctx.LastObservation != nil && ctx.LastObservation.Error != "" && !ctx.LastObservation.ShouldRetry {
		if isToolErrorIrrecoverable(ctx.LastObservation) {
			r.getLogger().Warn("termination: irrecoverable tool error",
				"error", ctx.LastObservation.Error)
			return true, "tool error: irrecoverable"
		}
	}

	if ctx.LastThought != nil && ctx.LastThought.IsFinal {
		r.getLogger().Debug("termination: thinker produced final answer")
		return true, "thinker produced final answer"
	}

	if ctx.LastAction != nil && ctx.LastThought.Decision == DecisionAnswer {
		r.getLogger().Debug("termination: direct answer produced")
		return true, "direct answer produced"
	}

	if ctx.LastAction != nil && ctx.LastThought.Decision == DecisionClarify {
		r.getLogger().Debug("termination: clarification needed")
		return true, "clarification needed"
	}

	if isDestructiveLoop(ctx.History) {
		r.getLogger().Warn("termination: destructive loop detected",
			"history_len", len(ctx.History))
		return true, "destructive loop detected: same tool call and error repeated"
	}

	if isAgentStuck(ctx.History) {
		r.getLogger().Warn("termination: agent stuck detected",
			"history_len", len(ctx.History))
		return true, "agent stuck: no tool progress in recent iterations"
	}

	if isResultConverged(ctx.History) {
		r.getLogger().Info("termination: result converged",
			"history_len", len(ctx.History))
		return true, "result converged"
	}

	if isDuplicateAction(ctx.History) {
		last := ctx.History[len(ctx.History)-1]
		prev := ctx.History[len(ctx.History)-2]
		r.getLogger().Warn("termination: duplicate action detected",
			"last_tools", collectToolNames(last.Action),
			"last_result_len", len(last.Action.Summary()),
			"prev_tools", collectToolNames(prev.Action),
			"prev_result_len", len(prev.Action.Summary()),
			"history_len", len(ctx.History),
			"iteration", ctx.CurrentIteration)
		return true, "duplicate action detected"
	}

	return false, ""
}

const (
	maxDestructiveLoopCount = 3
	maxStuckCount           = 4
)

func isToolErrorIrrecoverable(obs *Observation) bool {
	if obs == nil || obs.Error == "" {
		return false
	}
	irrecoverablePatterns := []string{
		"permission denied",
		"unauthorized",
		"invalid api key",
		"authentication failed",
		"access denied",
		"forbidden",
	}
	lower := strings.ToLower(obs.Error)
	for _, p := range irrecoverablePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isDestructiveLoop(history []Step) bool {
	if len(history) < maxDestructiveLoopCount {
		return false
	}
	tail := history[len(history)-maxDestructiveLoopCount:]
	firstSig := toolSignature(tail[0])
	firstErr := tail[0].Observation.Error
	if firstErr == "" {
		return false
	}
	for _, step := range tail[1:] {
		if step.Thought.Decision != DecisionAct {
			return false
		}
		if toolSignature(step) != firstSig {
			return false
		}
		if step.Observation.Error != firstErr {
			return false
		}
	}
	return true
}

func isAgentStuck(history []Step) bool {
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

func isResultConverged(history []Step) bool {
	if len(history) < 3 {
		return false
	}
	last3 := history[len(history)-3:]
	if last3[0].Action.Summary() == "" || last3[1].Action.Summary() == "" || last3[2].Action.Summary() == "" {
		return false
	}
	return last3[0].Action.Summary() == last3[1].Action.Summary() && last3[1].Action.Summary() == last3[2].Action.Summary()
}

func isDuplicateAction(history []Step) bool {
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
// Used by isDestructiveLoop and isDuplicateAction for multi-tool-aware comparison.
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
		if step.Thought.ToolCalls != nil {
			if params, ok := step.Thought.ToolCalls[name]; ok {
				paramStr = fmt.Sprintf("%v", params)
			}
		}
		parts = append(parts, name+":"+paramStr)
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, " "))
}

// collectToolNames returns a comma-separated list of tool names from an Action's Results.
func collectToolNames(a Action) string {
	names := make([]string, len(a.Results))
	for i, tr := range a.Results {
		names[i] = tr.ToolName
	}
	// Sort for deterministic output
	sort.Strings(names)
	return strings.Join(names, ",")
}

package reactor

import (
	"fmt"
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

	if ctx.LastAction != nil && ctx.LastAction.Type == ActionTypeAnswer {
		r.getLogger().Debug("termination: direct answer produced")
		return true, "direct answer produced"
	}

	if ctx.LastAction != nil && ctx.LastAction.Type == ActionTypeClarify {
		if ctx.LastAction.Target == "" {
			r.getLogger().Debug("termination: clarification needed")
			return true, "clarification needed"
		}
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
			"last_target", last.Action.Target,
			"last_params", fmt.Sprintf("%v", last.Action.Params),
			"last_result_len", len(last.Action.Result),
			"prev_target", prev.Action.Target,
			"prev_params", fmt.Sprintf("%v", prev.Action.Params),
			"prev_result_len", len(prev.Action.Result),
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
	var target, params, errMsg string
	for i, step := range tail {
		if step.Action.Type != ActionTypeToolCall {
			return false
		}
		if i == 0 {
			target = step.Action.Target
			params = fmt.Sprintf("%v", step.Action.Params)
			errMsg = step.Observation.Error
		} else {
			if step.Action.Target != target ||
				fmt.Sprintf("%v", step.Action.Params) != params ||
				step.Observation.Error != errMsg {
				return false
			}
		}
	}
	return errMsg != ""
}

func isAgentStuck(history []Step) bool {
	if len(history) < maxStuckCount {
		return false
	}
	count := 0
	for i := len(history) - 1; i >= 0 && i >= len(history)-maxStuckCount; i-- {
		if history[i].Action.Type != ActionTypeToolCall {
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
	if last3[0].Action.Result == "" || last3[1].Action.Result == "" || last3[2].Action.Result == "" {
		return false
	}
	return last3[0].Action.Result == last3[1].Action.Result && last3[1].Action.Result == last3[2].Action.Result
}

func isDuplicateAction(history []Step) bool {
	if len(history) < 2 {
		return false
	}
	last := history[len(history)-1]
	prev := history[len(history)-2]
	if last.Action.Type != ActionTypeToolCall || prev.Action.Type != ActionTypeToolCall {
		return false
	}
	if last.Action.Target != prev.Action.Target || last.Action.Result != prev.Action.Result {
		return false
	}
	lastParams := fmt.Sprintf("%v", last.Action.Params)
	prevParams := fmt.Sprintf("%v", prev.Action.Params)
	if lastParams != prevParams {
		return false
	}
	return true
}



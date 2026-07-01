package agents

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
)

type AskBuilder struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agentName string
	question  string
	session   *session.Session
	runtime   *Runtime
	onEvent   map[events.ReactEventType][]func(data any)
	resultErr error

	// onAnyEvent fires for every ReactEvent emitted by the execution loop,
	// before type-specific handlers (onEvent). Used by the daemon to track
	// agent_name across forwarded sub-agent events.
	onAnyEvent []func(events.ReactEvent)

	// parentEmit forwards ReactEvent to the parent EventBus.
	// When set (sub-agent), all events from this execution are also
	// emitted to the parent, enabling cross-agent event visibility.
	parentEmit func(events.ReactEvent)

	resultAnswer            string
	resultUsage             session.TokenUsage
	resultIterations        int
	resultDuration          time.Duration
	resultTerminationReason string
}

func (b *AskBuilder) on(typ events.ReactEventType, fn func(data any)) *AskBuilder {
	b.onEvent[typ] = append(b.onEvent[typ], fn)
	return b
}

func (b *AskBuilder) OnThinking(fn func(chunk string)) *AskBuilder {
	return b.on(events.ThinkingDelta, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnContent(fn func(chunk string)) *AskBuilder {
	return b.on(events.ContentDelta, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnToolUseDelta(fn func(data events.ToolUseDeltaData)) *AskBuilder {
	return b.on(events.ToolUseDelta, func(d any) {
		if v, ok := d.(events.ToolUseDeltaData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnThinkingDone(fn func()) *AskBuilder {
	return b.on(events.ThinkingDone, func(d any) { fn() })
}

func (b *AskBuilder) OnToolStart(fn func(data events.ToolExecStartData)) *AskBuilder {
	return b.on(events.ToolExecStart, func(d any) {
		if v, ok := d.(events.ToolExecStartData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnToolEnd(fn func(data events.ToolExecEndData)) *AskBuilder {
	return b.on(events.ToolExecEnd, func(d any) {
		if v, ok := d.(events.ToolExecEndData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnSubtaskSpawned(fn func(data events.SubtaskInfo)) *AskBuilder {
	return b.on(events.SubtaskSpawned, func(d any) {
		if v, ok := d.(events.SubtaskInfo); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnSubtaskCompleted(fn func(data events.SubtaskResult)) *AskBuilder {
	return b.on(events.SubtaskCompleted, func(d any) {
		if v, ok := d.(events.SubtaskResult); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionRequest(fn func(data events.PermissionRequestData)) *AskBuilder {
	return b.on(events.PermissionRequest, func(d any) {
		if v, ok := d.(events.PermissionRequestData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionDenied(fn func(reason string)) *AskBuilder {
	return b.on(events.PermissionDenied, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnAskUser(fn func(data events.AskUserRequestData)) *AskBuilder {
	return b.on(events.AskUserRequest, func(d any) {
		if v, ok := d.(events.AskUserRequestData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnAskUserPending(fn func(data events.AskUserPendingData)) *AskBuilder {
	return b.on(events.AskUserPending, func(d any) {
		if v, ok := d.(events.AskUserPendingData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionPending(fn func(data events.PermissionPendingData)) *AskBuilder {
	return b.on(events.PermissionPending, func(d any) {
		if v, ok := d.(events.PermissionPendingData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnExecutionSummary(fn func(data events.ExecutionSummaryData)) *AskBuilder {
	return b.on(events.ExecutionSummary, func(d any) {
		if v, ok := d.(events.ExecutionSummaryData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnCompaction(fn func(data events.CompactionData)) *AskBuilder {
	return b.on(events.Compaction, func(d any) {
		if v, ok := d.(events.CompactionData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnError(fn func(err string)) *AskBuilder {
	return b.on(events.Error, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnLLMTimeout(fn func(data events.LLMTimeoutData)) *AskBuilder {
	return b.on(events.LLMTimeout, func(d any) {
		if v, ok := d.(events.LLMTimeoutData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnCycleEnd(fn func(data events.CycleInfo)) *AskBuilder {
	return b.on(events.CycleEnd, func(d any) {
		if v, ok := d.(events.CycleInfo); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnTaskSummary(fn func(data events.TaskSummaryData)) *AskBuilder {
	return b.on(events.TaskSummary, func(d any) {
		if v, ok := d.(events.TaskSummaryData); ok {
			fn(v)
		}
	})
}

// OnTokenUsageRecorded registers a handler for per-LLM-call token usage updates.
// This fires after each individual LLM API call's tokens are persisted to the
// TokenUsageStore. Use this for real-time token usage tracking in the UI.
func (b *AskBuilder) OnTokenUsageRecorded(fn func(data session.TokenUsageRecord)) *AskBuilder {
	return b.on(events.TokenUsageRecorded, func(d any) {
		if v, ok := d.(session.TokenUsageRecord); ok {
			fn(v)
		}
	})
}

// OnEvent registers a catch-all handler that fires for every ReactEvent
// emitted by the execution loop, before type-specific handlers.
// The handler receives the full ReactEvent including AgentName, SessionID, Type, and Data.
// This is useful for tracking metadata like which agent produced an event.
func (b *AskBuilder) OnEvent(fn func(events.ReactEvent)) *AskBuilder {
	b.onAnyEvent = append(b.onAnyEvent, fn)
	return b
}

// OnMaxTurnsReached registers a handler for the MaxTurnsReached event.
// This event fires when the Think-Act loop reaches MaxTurns without producing
// a final answer. It is NOT an error - it's a normal boundary condition.
//
// Use this to display a friendly suggestion to the user instead of an error.
// Example:
//
//	result, err := rt.Ask("agent", "question", session).
//	    OnMaxTurnsReached(func(data events.MaxTurnsReachedData) {
//	        fmt.Println(data.Suggestion)
//	    }).
//	    Run()
func (b *AskBuilder) OnMaxTurnsReached(fn func(data events.MaxTurnsReachedData)) *AskBuilder {
	return b.on(events.MaxTurnsReached, func(d any) {
		if v, ok := d.(events.MaxTurnsReachedData); ok {
			fn(v)
		}
	})
}

// WithContext sets a custom context for the AskBuilder.
// If the context is cancelled, the execution loop stops.
// Cancel() will not cancel the custom context — callers should cancel it directly.
func (b *AskBuilder) WithContext(ctx context.Context) *AskBuilder {
	b.ctx = ctx
	return b
}

// Cancel stops the execution loop.
func (b *AskBuilder) Cancel() {
	if b.cancel != nil {
		b.cancel()
	}
}

func (b *AskBuilder) OnAnswer(fn func(answer string)) *AskBuilder {
	return b.on(events.FinalAnswer, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

// Run executes the ThinkingLoop and returns the result.
func (b *AskBuilder) Run() (*RunResult, error) {
	b.runtime.exec(b)
	if b.resultErr != nil {
		reason := b.resultTerminationReason
		if reason == "" {
			reason = "error"
		}
		return &RunResult{
			Answer:            b.resultAnswer,
			TokenUsage:        b.resultUsage,
			Duration:          b.resultDuration,
			Iterations:        b.resultIterations,
			TerminationReason: reason,
		}, b.resultErr
	}
	reason := b.resultTerminationReason
	if reason == "" {
		reason = "completed"
	}
	return &RunResult{
		Answer:            b.resultAnswer,
		TokenUsage:        b.resultUsage,
		Duration:          b.resultDuration,
		Iterations:        b.resultIterations,
		TerminationReason: reason,
	}, nil
}

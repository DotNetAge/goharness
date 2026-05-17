package reactor

import (
	"context"
	"time"
)

// RunSnapshot captures the serializable state of a T-A-O execution at a point in time.
// It is used by Pause/Resume to interrupt and later continue a running task.
// Runtime-only fields (context.Context, cancel func, emitEvent callback) are excluded.
type RunSnapshot struct {
	// Identity
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	ParentID  string `json:"parent_id"`

	// Input
	Input               string              `json:"input"`
	ConversationHistory ConversationHistory `json:"conversation_history"`

	// Progress
	CurrentIteration int `json:"current_iteration"`
	MaxIterations    int `json:"max_iterations"`

	// Last cycle state
	LastThought     *Thought     `json:"last_thought,omitempty"`
	LastAction      *Action      `json:"last_action,omitempty"`
	LastObservation *Observation `json:"last_observation,omitempty"`

	// Full history
	History []Step `json:"history,omitempty"`

	// Termination state
	Terminated        bool   `json:"terminated"`
	TerminationReason string `json:"termination_reason,omitempty"`

	// Metadata
	PausedAt time.Time `json:"paused_at"`
}

// ToSnapshot extracts a serializable snapshot from a ReactContext.
func (c *ReactContext) ToSnapshot() *RunSnapshot {
	// Copy history to avoid aliasing
	history := make([]Step, len(c.History))
	copy(history, c.History)

	// Copy conversation history
	conv := make(ConversationHistory, len(c.ConversationHistory))
	copy(conv, c.ConversationHistory)

	return &RunSnapshot{
		SessionID:           c.SessionID,
		TaskID:              c.TaskID,
		ParentID:            c.ParentID,
		Input:               c.Input,
		ConversationHistory: conv,
		CurrentIteration:    c.CurrentIteration,
		MaxIterations:       c.MaxIterations,
		LastThought:         c.LastThought,
		LastAction:          c.LastAction,
		LastObservation:     c.LastObservation,
		History:             history,
		Terminated:          c.IsTerminated,
		TerminationReason:   c.TerminationReason,
		PausedAt:            time.Now(),
	}
}

// NewReactContextFromSnapshot reconstructs a ReactContext from a snapshot.
// The provided ctx becomes the parent context; a new cancel func is created.
func NewReactContextFromSnapshot(ctx context.Context, snapshot *RunSnapshot) *ReactContext {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)

	// Restore history (was copied during ToSnapshot, safe to reuse)
	history := make([]Step, len(snapshot.History))
	copy(history, snapshot.History)

	// Restore conversation history
	conv := make(ConversationHistory, len(snapshot.ConversationHistory))
	copy(conv, snapshot.ConversationHistory)

	return &ReactContext{
		SessionID:           snapshot.SessionID,
		TaskID:              snapshot.TaskID,
		ParentID:            snapshot.ParentID,
		ctx:                 ctx,
		cancel:              cancel,
		Input:               snapshot.Input,
		ConversationHistory: conv,
		CurrentIteration:    snapshot.CurrentIteration,
		MaxIterations:       snapshot.MaxIterations,
		LastThought:         snapshot.LastThought,
		LastAction:          snapshot.LastAction,
		LastObservation:     snapshot.LastObservation,
		History:             history,
		IsTerminated:        snapshot.Terminated,
		TerminationReason:   snapshot.TerminationReason,
	}
}

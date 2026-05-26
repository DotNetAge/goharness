package reactor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// ConversationHistory is a typed slice of core.Message representing the
// sequential exchange between the user, assistant, and tools within a session.
// It is embedded in ReactContext for direct field access.
type ConversationHistory []core.Message

// Format renders the conversation history into a structured, human-readable text block.
// Each message is prefixed with its 1-based index, role, and optional timestamp.
//
// Parameters:
//   - maxTurns: limits the number of recent messages to include (0 = all messages).
//
// Returns the formatted string, or "(no conversation history)" if empty.
func (h ConversationHistory) Format(maxTurns int) string {
	if len(h) == 0 {
		return "(no conversation history)"
	}
	messages := h
	if maxTurns > 0 && len(messages) > maxTurns {
		messages = messages[len(messages)-maxTurns:]
	}
	var sb strings.Builder
	for i, msg := range messages {
		ts := ""
		if msg.Timestamp > 0 {
			ts = time.Unix(msg.Timestamp, 0).Format(" 15:04:05")
		}
		fmt.Fprintf(&sb, "  [%d] %s%s: %s\n", i+1, msg.Role, ts, msg.Content)
	}
	return sb.String()
}

// ReactContext holds the shared mutable state for a single Reactor.Run invocation.
// It is created at the start of Run and mutated throughout the Think-Act loop.
// All fields are protected by mu for concurrent read safety where needed.
type ReactContext struct {

	SessionID string
	// SessionID uniquely identifies the conversation session across runs.

	TaskID string
	// TaskID identifies this execution: "main" for the primary reactor,
	// "task_N" for sub-agent delegations.

	ParentID string
	// ParentID is the parent task ID; empty for "main" tasks.

	ctx context.Context
	// ctx is the Go context for cancellation and timeout propagation.

	cancel context.CancelFunc
	// cancel is the cancellation function derived from ctx.

	CurrentIteration int
	// CurrentIteration tracks the current Think-Act cycle index (0-based).

	MaxIterations int
	// MaxIterations is the upper bound on the number of Think-Act cycles allowed.

	Input string
	// Input is the original user prompt that initiated this run.

	ConversationHistory
	// ConversationHistory embeds the message exchange slice directly.

	LastThought *Thought
	// LastThought holds the output of the most recent Think phase.

	LastAction *Action
	// LastAction holds the output of the most recent Act phase.

	History []Step
	// History accumulates all completed Step records for the run.

	CurrentInputTokens int
	// CurrentInputTokens tracks token usage for the current iteration.

	IsTerminated bool
	// IsTerminated signals that the run has been stopped (by limit, error, or explicit termination).

	TerminationReason string
	// TerminationReason explains why the run was terminated (e.g., "max_iterations_reached").

	Mode AgentMode
	// Mode controls the agent's behavior mode (e.g., ModeExecutor).
	// Defaults to ModeExecutor.

	emitEvent func(event core.ReactEvent)
	// emitEvent is the event callback set by the Reactor before Run.
	// If non-nil, it is called after each Think-Act phase to emit events.
	// It is a no-op if nil.

	mu sync.RWMutex
	// mu is write-locked by AppendHistory/AddMessage/AddToolMessage.
	// Reads of History/ConversationHistory assume single-goroutine access
	// and do not acquire the read lock.
}

// EmitEvent publishes a ReactEvent through the context's configured event callback.
// The event is automatically annotated with SessionID, TaskID, and ParentID.
//
// Parameters:
//   - eventType: the type of event being emitted (e.g., core.EventThinkComplete).
//   - data: arbitrary payload associated with the event.
//
// This method is a no-op if no event bus (emitEvent) is configured.
func (c *ReactContext) EmitEvent(eventType core.ReactEventType, data any) {
	if c.emitEvent == nil {
		return
	}
	c.emitEvent(core.NewReactEvent(c.SessionID, c.TaskID, c.ParentID, eventType, data))
}

// NewReactContext creates a new ReactContext for a Run invocation with default identity ("main").
//
// Parameters:
//   - ctx: the parent context (nil defaults to context.Background()).
//   - input: the user's original prompt.
//   - history: prior conversation messages to continue from.
//   - maxIter: maximum number of Think-Act iterations (≤0 defaults to core.DefaultMaxSteps).
//
// Returns a pointer to the initialized ReactContext.
func NewReactContext(ctx context.Context, input string, history ConversationHistory, maxIter int) *ReactContext {
	return NewReactContextWithIDs(ctx, "main", "", input, history, maxIter)
}

// NewReactContextWithIDs creates a ReactContext with explicit task identity for
// sub-agent delegations or custom task tracking.
//
// Parameters:
//   - ctx: the parent context (nil defaults to context.Background()).
//   - taskID: identifier for this task (e.g., "task_1").
//   - parentID: identifier of the parent task (empty for root tasks).
//   - input: the user's original prompt or delegated task description.
//   - history: prior conversation messages.
//   - maxIter: maximum number of Think-Act iterations (≤0 defaults to core.DefaultMaxSteps).
//
// Returns a pointer to the initialized ReactContext.
func NewReactContextWithIDs(ctx context.Context, taskID, parentID, input string, history ConversationHistory, maxIter int) *ReactContext {
	if maxIter <= 0 {
		maxIter = core.DefaultMaxSteps
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &ReactContext{
		ctx:                 ctx,
		cancel:              cancel,
		TaskID:              taskID,
		ParentID:            parentID,
		Input:               input,
		ConversationHistory: history,
		MaxIterations:       maxIter,
		History:             make([]Step, 0, maxIter),
		Mode:                ModeExecutor,
	}
}

// Ctx returns the Go context.Context associated with this run.
// Returns context.Background() if no context was set.
func (c *ReactContext) Ctx() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// withToolTimeout creates a shallow copy of ReactContext with a timeout-wrapped context.
// The original ReactContext is not modified. This is used by executeSyncTools to
// prevent a hanging tool from blocking the entire Think-Act loop indefinitely.
func (c *ReactContext) withToolTimeout(timeout time.Duration) (*ReactContext, context.CancelFunc) {
	toolCtx, cancel := context.WithTimeout(c.Ctx(), timeout)
	return &ReactContext{
		SessionID:             c.SessionID,
		TaskID:                c.TaskID,
		ParentID:              c.ParentID,
		ctx:                   toolCtx,
		cancel:                cancel,
		CurrentIteration:      c.CurrentIteration,
		MaxIterations:         c.MaxIterations,
		Input:                 c.Input,
		ConversationHistory:   c.ConversationHistory,
		LastThought:           c.LastThought,
		LastAction:            c.LastAction,
		History:               c.History,
		CurrentInputTokens:    c.CurrentInputTokens,
		IsTerminated:          c.IsTerminated,
		TerminationReason:     c.TerminationReason,
		Mode:                  c.Mode,
		emitEvent:             c.emitEvent,
	}, cancel
}

// Cancel triggers cancellation of the run's context, signaling all
// long-running operations (LLM calls, tool executions) to stop.
func (c *ReactContext) Cancel() {
	if c.cancel != nil {
		c.cancel()
	}
}

// AppendHistory thread-safely adds a completed Step to the run history.
// The step is appended in iteration order for later inspection or replay.
func (c *ReactContext) AppendHistory(step Step) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.History = append(c.History, step)
}

// AddMessage thread-safely appends a new message to the conversation history.
//
// Parameters:
//   - role: the message role (e.g., "user", "assistant", "system").
//   - content: the message body text.
func (c *ReactContext) AddMessage(role, content string, toolCalls ...core.ToolCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tc []core.ToolCall
	if len(toolCalls) > 0 {
		tc = toolCalls
	}
	c.ConversationHistory = append(c.ConversationHistory, core.Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Unix(),
		ToolCalls: tc,
	})
}

// AddToolMessage thread-safely appends a tool result message to the conversation history.
// This is used to feed tool execution results back into the LLM context.
//
// Parameters:
//   - role: the message role (typically "tool").
//   - content: the tool result output string.
//   - toolCallID: the original function call ID from the LLM response, used for correlation.
func (c *ReactContext) AddToolMessage(role, content, toolCallID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ConversationHistory = append(c.ConversationHistory, core.Message{
		Role:       role,
		Content:    content,
		Timestamp:  time.Now().Unix(),
		ToolCallID: toolCallID,
	})
}

package reactor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// ConversationHistory is a typed slice of core.Message for conversation history.
type ConversationHistory []core.Message

// Format renders the conversation history into a structured text block.
// maxTurns limits the number of recent messages included (0 = all).
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

// ReactContext holds the shared state for a single Run invocation.
// It is created at the start of Run and mutated throughout the T-A-O loop.
type ReactContext struct {
	// Identity
	SessionID string // identifies the conversation session
	TaskID    string // "main" for primary reactor, "task_N" for subagents
	ParentID  string // parent task ID, empty for "main"

	// Lifecycle
	ctx              context.Context
	cancel           context.CancelFunc
	CurrentIteration int
	MaxIterations    int

	// Input
	Input string
	ConversationHistory

	// Last cycle results
	LastThought           *Thought
	LastAction            *Action
	LastObservation       *Observation
	History               []Step

	// Token tracking for current iteration
	CurrentInputTokens int

	// Termination
	IsTerminated      bool
	TerminationReason string

	// Mode defaults to ModeExecutor.
	Mode AgentMode
	// Event callback — set by the Reactor before Run.
	// If non-nil, called after each T-A-O phase to emit events.
	emitEvent func(event core.ReactEvent)

	// Thread safety for concurrent read access
	mu sync.RWMutex
}

// EmitEvent publishes a ReactEvent through the context's event callback.
// It is a no-op if no event bus is configured.
func (c *ReactContext) EmitEvent(eventType core.ReactEventType, data any) {
	if c.emitEvent == nil {
		return
	}
	c.emitEvent(core.NewReactEvent(c.SessionID, c.TaskID, c.ParentID, eventType, data))
}

// NewReactContext creates a new ReactContext for a Run invocation.
// If ctx is nil, context.Background() is used.
func NewReactContext(ctx context.Context, input string, history ConversationHistory, maxIter int) *ReactContext {
	return NewReactContextWithIDs(ctx, "main", "", input, history, maxIter)
}

// NewReactContextWithIDs creates a ReactContext with explicit task identity.
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
		Mode:                ModeExecutor, // Default: executor mode
	}
}

// Ctx returns the context.Context for this run.
func (c *ReactContext) Ctx() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// Cancel cancels the run context.
func (c *ReactContext) Cancel() {
	if c.cancel != nil {
		c.cancel()
	}
}

// AppendHistory adds a completed step to the history.
func (c *ReactContext) AppendHistory(step Step) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.History = append(c.History, step)
}

// AddMessage appends a message to the conversation history.
func (c *ReactContext) AddMessage(role, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ConversationHistory = append(c.ConversationHistory, core.Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Unix(),
	})
}

// AddToolMessage appends a tool result message with its tool_call_id.
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



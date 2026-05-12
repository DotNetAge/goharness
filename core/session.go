package core

import (
	"context"
	"errors"
	"time"
)

// TokenUsage records the token consumption for a single LLM call.
// It is both returned in CallResult and persisted via SessionStore for billing/monitoring.
type TokenUsage struct {
	Timestamp    time.Time `json:"timestamp"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	RemainTokens int       `json:"remain_tokens"`
}

// SlideEvent is emitted when the ContextWindow slides out old messages.
// It contains the messages that were evicted, so consumers (e.g. RAG/Memory)
// can semantically process them into long-term knowledge.
type SlideEvent struct {
	SessionID string    `json:"session_id"`
	Slided    []Message `json:"slided"`
	Remaining int       `json:"remaining"`
	Timestamp int64     `json:"timestamp"`
}

// SlideHandler is the callback type for consuming slide events.
// Implementations can store slid messages into RAG or other long-term storage.
type SlideHandler func(ctx context.Context, event SlideEvent)

// SessionInfo holds metadata about a session, used by ListSessions and GetByRole.
// It includes directory context that is essential for tool execution and prompt generation.
type SessionInfo struct {
	SessionID      string    `json:"session_id"`
	AgentName      string    `json:"agent_name,omitempty"`
	ProjectDir     string    `json:"project_dir,omitempty"` // Working directory at session creation time
	SessionDir     string    `json:"session_dir,omitempty"` // Session sandbox directory (managed by Store)
	Messages       []Message `json:"messages"`
	LastActivityAt time.Time `json:"last_activity_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// GetProjectDir returns the project working directory for this session.
// Returns empty string if not set (callers should fallback to os.Getwd()).
func (s *SessionInfo) GetProjectDir() string {
	return s.ProjectDir
}

// GetSessionDir returns the session sandbox directory for this session.
// This directory is isolated per-session and managed by the SessionStore implementation.
func (s *SessionInfo) GetSessionDir() string {
	return s.SessionDir
}

// SessionStore is the persistence interface for conversation history (WAL mode).
// It stores messages in order and provides token-budget-aware context retrieval.
//
// Responsibilities:
//   - Append/Retrieve ordered message history per session
//   - CurrentContext returns messages that fit within a token budget (sliding-window read side)
//   - Notify consumers via SlideHandler when messages are evicted from ContextWindow
//   - Manage session lifecycle (Create, GetMeta, directory resolution)
//
// It does NOT do semantic analysis — that is Memory/RAG's job.
type SessionStore interface {
	// Append adds a message to the end of the specified session's history.
	Append(ctx context.Context, sessionID string, agentName string, message Message) error

	// Get returns all messages for the specified session (complete history).
	Get(ctx context.Context, sessionID string) ([]Message, error)

	// CurrentContext returns messages suitable for the current context window,
	// selecting from newest to oldest until total tokens <= maxTokens.
	CurrentContext(ctx context.Context, agentName string, maxTokens int64) ([]Message, error)

	// Delete removes a message by timestamp from the specified session.
	Delete(ctx context.Context, timestamp int64, sessionID string) error

	// Clear removes all messages for the specified session (session reset).
	Clear(ctx context.Context, sessionID string) error

	// SetSlideHandler registers a callback for slide events.
	SetSlideHandler(handler SlideHandler)

	// Close releases any resources held by the store.
	Close() error

	// GetByRole returns the most recent SessionInfo for the given role,
	// or ErrSessionNotFound if no session exists for that role.
	// This is used by Agent.Switch() to resume the latest session for a role
	// instead of creating a new one each time.
	GetByRole(ctx context.Context, agent string) (*SessionInfo, error)

	// ListSessions returns metadata for all sessions, sorted by LastActivityAt descending (newest first).
	ListSessions(ctx context.Context) ([]SessionInfo, error)

	// AppendTokenUsage persists a token usage record for a session.
	// Called by LLMCaller after each completed Call/CallStream/CallGate.
	AppendTokenUsage(ctx context.Context, sessionID string, usage TokenUsage) error

	// GetTokenUsages retrieves all token usage records for a session.
	// Used for billing, monitoring, and external token statistics.
	GetTokenUsages(ctx context.Context, sessionID string) ([]TokenUsage, error)

	// === Session Lifecycle Management (Framework-level directory control) ===

	// Create creates a new session with the given agent name and options.
	// The implementation should:
	//   1. Generate a unique session ID
	//   2. Capture or accept ProjectDir (working directory at creation time)
	//   3. Calculate and create SessionDir (sandbox directory: base/<agent>/<session_id>)
	//   4. Return a complete SessionInfo with directory context
	//
	// This centralizes session creation logic that was previously scattered across application layers.
	Create(ctx context.Context, agentName string, opts ...SessionOption) (*SessionInfo, error)

	// GetMeta returns complete session metadata including directory information.
	// Unlike GetByRole which returns minimal info, this includes ProjectDir and SessionDir.
	GetMeta(ctx context.Context, sessionID string) (*SessionInfo, error)

	// ResolveSessionDir returns the filesystem path for the session's sandbox directory.
	// Returns ErrSessionNotFound if the session does not exist.
	// This is the canonical way for tools and components to locate session-specific files.
	ResolveSessionDir(sessionID string) (string, error)
}

// SessionOption is a functional option for configuring session creation.
type SessionOption func(*SessionInfo)

// WithProjectDir sets the project working directory for a new session.
// If not provided, the implementation should use os.Getwd() as default.
func WithProjectDir(dir string) SessionOption {
	return func(s *SessionInfo) {
		s.ProjectDir = dir
	}
}

// NoopSlideHandler is a no-op SlideHandler for implementations that don't need it.
func NoopSlideHandler(_ context.Context, _ SlideEvent) {}

// EmitSlideEvent safely invokes the stored handler if non-nil.
func EmitSlideEvent(handler SlideHandler, ctx context.Context, event SlideEvent) {
	if handler != nil {
		handler(ctx, event)
	}
}

// ErrSessionNotFound is returned by GetByRole when no session exists for the given role.
var ErrSessionNotFound = errors.New("session not found for role")

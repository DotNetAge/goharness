package session

import (
	"context"
	"errors"
	"time"
)

// TokenUsage records the token consumption for a single LLM call.
// It is used as a transfer value type in events and LLM streaming.
// For persistent storage with grouping dimensions, use TokenUsageRecord instead.
//
// Detailed fields (CachedTokens, ReasoningTokens, etc.) are provider-specific.
// When the provider returns detailed token breakdowns (e.g., OpenAI's
// prompt_tokens_details / completion_tokens_details), they are preserved here.
// Fields that are not reported by the provider remain at their zero value.
type TokenUsage struct {
	Timestamp    time.Time `json:"timestamp"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	RemainTokens int       `json:"remain_tokens"`

	// CachedTokens is the number of tokens read from the prompt cache
	// (prompt_tokens_details.cached_tokens).
	CachedTokens int `json:"cached_tokens,omitempty"`

	// ReasoningTokens is the number of tokens used for reasoning/thinking
	// (completion_tokens_details.reasoning_tokens).
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	// AudioTokensInput is the number of audio tokens in the prompt
	// (prompt_tokens_details.audio_tokens).
	AudioTokensInput int `json:"audio_tokens_input,omitempty"`

	// AudioTokensOutput is the number of audio tokens in the completion
	// (completion_tokens_details.audio_tokens).
	AudioTokensOutput int `json:"audio_tokens_output,omitempty"`

	// AcceptedPredictionTokens is the number of tokens accepted from speculation
	// (completion_tokens_details.accepted_prediction_tokens).
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`

	// RejectedPredictionTokens is the number of tokens rejected from speculation
	// (completion_tokens_details.rejected_prediction_tokens).
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
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
	Title          string    `json:"title,omitempty"` // First user message content (for session list display)
	ProjectDir     string    `json:"project_dir,omitempty"` // Working directory at session creation time
	SessionDir     string    `json:"session_dir,omitempty"` // Session sandbox directory (managed by Store)
	Messages       []Message `json:"messages"`
	ModifyFiles    []string  `json:"modify_files,omitempty"`  // Tracked modified file paths
	LastActivityAt time.Time `json:"last_activity_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type SessionStore interface {
	Append(ctx context.Context, sessionID string, agentName string, message Message) error
	Get(ctx context.Context, sessionID string) ([]Message, error)
	CurrentContext(ctx context.Context, agentName string, maxTokens int64) ([]Message, error)
	Delete(ctx context.Context, timestamp int64, sessionID string) error
	Clear(ctx context.Context, sessionID string) error
	SetSlideHandler(handler SlideHandler)
	Close() error
	GetByRole(ctx context.Context, agent string) (*SessionInfo, error)
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	Create(ctx context.Context, agentName string, opts ...SessionOption) (*SessionInfo, error)
	GetMeta(ctx context.Context, sessionID string) (*SessionInfo, error)
	ResolveSessionDir(sessionID string) (string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	GetCursor(ctx context.Context, sessionID string) (int, error)
	SetCursor(ctx context.Context, sessionID string, cursor int) error

	// ModifyFiles persistence for file change tracking
	SaveModifyFiles(sessionID string, files []string) error
	GetModifyFiles(sessionID string) ([]string, error)
}

// Cursor persistence for compaction state recovery.
// These methods are used internally by Session to save/restore the cursor position.
// They are NOT part of the public Session API - external code should never call these.
//
// When compaction occurs (via tryCompact/executeCompactionPlan), the cursor advances.
// Without persisting it, a new Session object would load all messages but start with
// cursor=0, causing Current() to return too many messages (exceeding token limits).
//
// Implementation notes:
//   - FileSessionStore: stores cursor in meta.json
//   - MemorySessionStore: stores cursor in memory map
//   - GetCursor returns 0 if no cursor has been set (no compaction occurred)
type cursorCursorMethods struct{}

// CursorStore is an optional interface for session stores that support cursor persistence.
// Stores may implement this to support cursor-based compaction.
type CursorStore interface {
	GetCursor(ctx context.Context, sessionID string) (int, error)
	SetCursor(ctx context.Context, sessionID string, cursor int) error
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

var ErrSessionNotFound = errors.New("session not found for role")

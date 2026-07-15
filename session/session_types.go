package session

import (
	"context"
	"errors"
	"time"
)

// TokenUsage records the token consumption for a single LLM call.
// Field names align with the standard OpenAI-compatible API Usage response format.
// For persistent storage with grouping dimensions, use TokenUsageRecord instead.
type TokenUsage struct {
	Timestamp        time.Time `json:"timestamp" yaml:"timestamp"`
	PromptTokens     int       `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens" yaml:"total_tokens"`

	// CachedTokens is the number of cached prompt tokens
	// (prompt_tokens_details.cached_tokens / prompt_cache_hit_tokens).
	CachedTokens int `json:"cached_tokens,omitempty" yaml:"cached_tokens"`

	// ReasoningTokens is the number of thinking/reasoning tokens in the output
	// (completion_tokens_details.reasoning_tokens).
	ReasoningTokens int `json:"reasoning_tokens,omitempty" yaml:"reasoning_tokens"`
}

// ActualTokens returns the actual net token consumption excluding cache hits.
func (u TokenUsage) ActualTokens() int {
	n := u.PromptTokens + u.CompletionTokens - u.CachedTokens
	if n < 0 {
		return 0
	}
	return n
}

// PricingUnit defines per-model token pricing (per 1M tokens).
type PricingUnit struct {
	InputPricePer1M  float64
	OutputPricePer1M float64
}

// Cost calculates the monetary cost using the given pricing.
// Matches mindx/internal/core.CalculateCost — the canonical pricing algorithm.
// Cached tokens reduce the chargeable input rather than being billed separately.
func (u TokenUsage) Cost(p PricingUnit) float64 {
	netInput := u.PromptTokens - u.CachedTokens
	if netInput < 0 {
		netInput = 0
	}
	return float64(netInput)/1_000_000*p.InputPricePer1M +
		float64(u.CompletionTokens)/1_000_000*p.OutputPricePer1M
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

// SessionInfo holds metadata about a session, used by ListSessions and GetMeta.
// It includes directory context that is essential for tool execution and prompt generation.
type SessionInfo struct {
	SessionID      string    `json:"session_id"`
	AgentName      string    `json:"agent_name,omitempty"`
	Sponsor        string    `json:"sponsor,omitempty"` // Agent that created this session (empty = user-initiated)
	Title          string    `json:"title,omitempty"` // First user message content (for session list display)
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`         // Last meta.json save time
	LastActivityAt time.Time `json:"last_activity_at"`   // Last message activity timestamp
	ProjectDir     string    `json:"project_dir,omitempty"` // Working directory at session creation time
	SessionDir     string    `json:"session_dir,omitempty"` // Session sandbox directory (managed by Store)
	MessageCount   int       `json:"message_count"`         // Total messages in session
	Cursor         int       `json:"cursor"`                // Compaction cursor position (0 = no compaction)
	Messages       []Message `json:"messages,omitempty"`
	ModifyFiles    []string  `json:"modify_files,omitempty"`  // Tracked modified file paths
}

type SessionStore interface {
	Append(ctx context.Context, sessionID string, agentName string, sponsor string, message Message) error
	Get(ctx context.Context, sessionID string) ([]Message, error)
	CurrentContext(ctx context.Context, agentName string, maxTokens int64) ([]Message, error)
	Delete(ctx context.Context, timestamp int64, sessionID string) error
	Clear(ctx context.Context, sessionID string) error
	SetSlideHandler(handler SlideHandler)
	Close() error
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

	// UpdateMessages persists modifications to existing messages (e.g. MicroCompact
	// changes to the Compacted field). Takes the current cursor and full message list.
	// The store replaces the existing messages for the session atomically.
	UpdateMessages(ctx context.Context, sessionID string, cursor int, messages []Message) error

		// Truncate removes messages after keepCount, keeping only the first keepCount messages.
		Truncate(ctx context.Context, sessionID string, keepCount int) error
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

// WithProjectDirOption sets the project working directory for a new session info.
// If not provided, the implementation should use os.Getwd() as default.
func WithProjectDirOption(dir string) SessionOption {
	return func(s *SessionInfo) {
		s.ProjectDir = dir
	}
}

// WithSponsorOption sets the sponsor agent for a new session info.
// Sponsor identifies the agent that created/sponsored this session.
// Empty means user-initiated.
func WithSponsorOption(sponsor string) SessionOption {
	return func(s *SessionInfo) {
		s.Sponsor = sponsor
	}
}

// NoopSlideHandler is a no-op SlideHandler for implementations that don't need it.
func NoopSlideHandler(_ context.Context, _ SlideEvent) {}

var ErrSessionNotFound = errors.New("未找到对应角色的会话")

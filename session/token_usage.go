package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenUsageRecord represents a single LLM API call's token usage with grouping dimensions.
// It follows the write-only strategy: inserted once and never modified.
type TokenUsageRecord struct {
	// ID is a unique identifier for this record.
	ID string `json:"id"`

	// --- Grouping Dimensions ---
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	ModelName      string `json:"model_name"`
	ProviderName   string `json:"provider_name"`
	AgentName      string `json:"agent_name"`

	// --- Token Counts ---
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// Timestamp is the time when the LLM API call completed.
	Timestamp time.Time `json:"timestamp"`
}

// TokenUsageFilter defines query dimensions for retrieving usage records.
// Empty/zero fields mean "any" (no filter on that dimension).
type TokenUsageFilter struct {
	SessionID      string
	ConversationID string
	ModelName      string
	ProviderName   string
	AgentName      string
	Since          time.Time
	Until          time.Time
}

// TokenUsageStore is the storage interface for token usage records.
//
// goharness is a development framework — the storage implementation is injected
// from outside. Implementations may use SQLite, PostgreSQL, or any other backend.
// An in-memory implementation is provided as a framework fallback default.
//
// Design principles:
//   - Write-only: records are INSERT-only, never updated or deleted.
//   - Multi-dimension query: use TokenUsageFilter for flexible aggregation.
type TokenUsageStore interface {
	// Append writes a single usage record. Write-only: records are never modified or deleted.
	Append(ctx context.Context, record TokenUsageRecord) error

	// Query retrieves usage records matching the given filter.
	// Returns all matching records in insertion order.
	Query(ctx context.Context, filter TokenUsageFilter) ([]TokenUsageRecord, error)

	// Close cleans up resources held by the store.
	Close() error
}

// InMemoryTokenUsageStore provides an in-memory implementation of TokenUsageStore.
// This is the default fallback when no external store is injected.
// Thread-safe for concurrent access.
type InMemoryTokenUsageStore struct {
	mu      sync.RWMutex
	records []TokenUsageRecord
}

// NewInMemoryTokenUsageStore creates a new InMemoryTokenUsageStore.
func NewInMemoryTokenUsageStore() *InMemoryTokenUsageStore {
	return &InMemoryTokenUsageStore{}
}

// Append appends a usage record to the in-memory store.
func (s *InMemoryTokenUsageStore) Append(_ context.Context, record TokenUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

// Query retrieves usage records matching the given filter.
func (s *InMemoryTokenUsageStore) Query(_ context.Context, filter TokenUsageFilter) ([]TokenUsageRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []TokenUsageRecord
	for _, r := range s.records {
		if filter.SessionID != "" && r.SessionID != filter.SessionID {
			continue
		}
		if filter.ConversationID != "" && r.ConversationID != filter.ConversationID {
			continue
		}
		if filter.ModelName != "" && r.ModelName != filter.ModelName {
			continue
		}
		if filter.ProviderName != "" && r.ProviderName != filter.ProviderName {
			continue
		}
		if filter.AgentName != "" && r.AgentName != filter.AgentName {
			continue
		}
		if !filter.Since.IsZero() && r.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && r.Timestamp.After(filter.Until) {
			continue
		}
		result = append(result, r)
	}

	out := make([]TokenUsageRecord, len(result))
	copy(out, result)
	return out, nil
}

// Close is a no-op for the in-memory store.
func (s *InMemoryTokenUsageStore) Close() error {
	return nil
}

// NoopTokenUsageStore is a no-op implementation of TokenUsageStore.
// Used when token usage tracking is disabled.
type NoopTokenUsageStore struct{}

// NewNoopTokenUsageStore creates a new NoopTokenUsageStore.
func NewNoopTokenUsageStore() *NoopTokenUsageStore {
	return &NoopTokenUsageStore{}
}

// Append is a no-op.
func (s *NoopTokenUsageStore) Append(_ context.Context, _ TokenUsageRecord) error {
	return nil
}

// Query returns an empty result.
func (s *NoopTokenUsageStore) Query(_ context.Context, _ TokenUsageFilter) ([]TokenUsageRecord, error) {
	return nil, nil
}

// Close is a no-op.
func (s *NoopTokenUsageStore) Close() error {
	return nil
}

// NewRecordID generates a unique ID for a token usage record.
// Format: "tur_<nanoseconds>" (tur = token usage record).
func NewRecordID() string {
	return fmt.Sprintf("tur_%d", time.Now().UnixNano())
}

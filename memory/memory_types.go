// Package memory provides interfaces and types for AI agent memory systems.
// It supports both session-scoped memory (ephemeral, conversation-bound) and
// long-term memory (persistent knowledge storage) with configurable retrieval options.
package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Filter keys for memory retrieval scoping.
// These are used as Query.AddFilter keys to scope retrieval to specific agents/sessions.
const (
	FilterKeyAgentName = "agent_name"
	FilterKeySessionID = "session_id"
)

// MemoryChunk represents a single memory piece with full metadata.
// It is the core data structure for the memory system, distinct from
// the knowledge base (graph) storage.
type MemoryChunk struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content"`
	AgentName string    `json:"agent_name"`
	SessionID string    `json:"session_id"`
	Tags      []string  `json:"tags"`
	Timestamp time.Time `json:"timestamp"`
}

// ErrMemoryNotFound is returned when a requested memory record doesn't exist.
var ErrMemoryNotFound = errors.New("memory not found")

// ErrMemoryStorage is returned when a memory storage operation fails.
var ErrMemoryStorage = errors.New("memory storage failed")

// ErrMemoryRetrieval is returned when a memory retrieval operation fails.
var ErrMemoryRetrieval = errors.New("memory retrieval failed")

// MemoryType defines the type of memory for retrieval filtering.
type MemoryType int

const (
	// MemoryTypeSession represents ephemeral session-scoped memory.
	// Cleared when the session ends.
	MemoryTypeSession MemoryType = iota

	// MemoryTypeLongTerm represents persistent long-term knowledge.
	// Survives across sessions.
	MemoryTypeLongTerm
)

// Memory defines the interface for memory storage and retrieval operations.
type Memory interface {
	// Retrieve searches for memory chunks matching the query with optional filters.
	Retrieve(ctx context.Context, query string, opts ...RetrieveOption) ([]MemoryChunk, error)
	// Store persists a new memory chunk and returns its ID.
	Store(ctx context.Context, chunk MemoryChunk) (string, error)
	// Delete removes a memory chunk by ID.
	Delete(ctx context.Context, id string) error
}

// DefaultRetrieveConfig returns the default configuration for memory retrieval.
func DefaultRetrieveConfig() RetrieveConfig {
	return RetrieveConfig{Limit: 5}
}

// FormatMemoryRecords formats memory chunks into a human-readable string
// suitable for inclusion in AI prompts.
func FormatMemoryRecords(chunks []MemoryChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range chunks {
		if c.Summary != "" {
			sb.WriteString("## ")
			sb.WriteString(c.Summary)
			sb.WriteString("\n")
		}
		sb.WriteString(c.Content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

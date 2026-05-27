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

// ErrMemoryNotFound is returned when a requested memory record doesn't exist.
var ErrMemoryNotFound = errors.New("memory not found")

// ErrMemoryStorage is returned when a memory storage operation fails.
var ErrMemoryStorage = errors.New("memory storage failed")

// ErrMemoryRetrieval is returned when a memory retrieval operation fails.
var ErrMemoryRetrieval = errors.New("memory retrieval failed")

// MemoryType defines the type of memory record.
type MemoryType int

const (
	// MemoryTypeSession represents ephemeral session-scoped memory.
	// Cleared when the session ends.
	MemoryTypeSession MemoryType = iota

	// MemoryTypeLongTerm represents persistent long-term knowledge.
	// Survives across sessions.
	MemoryTypeLongTerm
)

// MemoryRecord represents a single memory entry with metadata.
type MemoryRecord struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id,omitempty"`
	Type      MemoryType `json:"type"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Tags      []string   `json:"tags,omitempty"`
	Score     float64    `json:"score,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Memory defines the interface for memory storage and retrieval operations.
type Memory interface {
	// Retrieve searches for memory records matching the query with optional filters.
	Retrieve(ctx context.Context, query string, opts ...RetrieveOption) ([]MemoryRecord, error)
	// Store persists a new memory record and returns its ID.
	Store(ctx context.Context, record MemoryRecord) (string, error)
	// Delete removes a memory record by ID.
	Delete(ctx context.Context, id string) error
}

// DefaultRetrieveConfig returns the default configuration for memory retrieval.
func DefaultRetrieveConfig() RetrieveConfig {
	return RetrieveConfig{Limit: 5}
}

// FormatMemoryRecords formats memory records into a human-readable string
// suitable for inclusion in AI prompts. Uses Markdown headings for titles.
func FormatMemoryRecords(records []MemoryRecord) string {
	if len(records) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, r := range records {
		typeName := memoryTypeLabel(r.Type)
		if r.Title != "" {
			sb.WriteString("## ")
			sb.WriteString(typeName)
			sb.WriteString(": ")
			sb.WriteString(r.Title)
			sb.WriteString("\n")
		}
		sb.WriteString(r.Content)
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String())
}

// memoryTypeLabel returns a human-readable label for a memory type.
func memoryTypeLabel(t MemoryType) string {
	switch t {
	case MemoryTypeSession:
		return "Session Memory"
	case MemoryTypeLongTerm:
		return "Long-term Knowledge"
	default:
		return "Unknown"
	}
}


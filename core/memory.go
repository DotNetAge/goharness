package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

var (
	ErrMemoryNotFound  = errors.New("memory not found")
	ErrMemoryStorage   = errors.New("memory storage failed")
	ErrMemoryRetrieval = errors.New("memory retrieval failed")
)

type MemoryType int

const (
	MemoryTypeSession  MemoryType = iota
	MemoryTypeLongTerm
)

type MemoryRecord struct {
	ID        string      `json:"id"`
	SessionID string      `json:"session_id,omitempty"`
	Type      MemoryType  `json:"type"`
	Title     string      `json:"title"`
	Content   string      `json:"content"`
	Tags      []string    `json:"tags,omitempty"`
	Score     float64     `json:"score,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}

type Memory interface {
	Retrieve(ctx context.Context, query string, opts ...RetrieveOption) ([]MemoryRecord, error)
	Store(ctx context.Context, record MemoryRecord) (string, error)
	Delete(ctx context.Context, id string) error
}

type RetrieveConfig struct {
	Types     []MemoryType
	SessionID string
	Limit     int
	MinScore  float64
}

type RetrieveOption func(*RetrieveConfig)

func WithMemoryTypes(types ...MemoryType) RetrieveOption {
	return func(c *RetrieveConfig) { c.Types = types }
}

func WithMemoryLimit(n int) RetrieveOption {
	return func(c *RetrieveConfig) {
		if n > 0 { c.Limit = n }
	}
}

func WithMinScore(score float64) RetrieveOption {
	return func(c *RetrieveConfig) { c.MinScore = score }
}

// WithMemorySessionID scopes memory retrieval to a specific session.
// Memory implementations should filter by this field for session-scoped recall.
func WithMemorySessionID(sessionID string) RetrieveOption {
	return func(c *RetrieveConfig) { c.SessionID = sessionID }
}

func DefaultRetrieveConfig() RetrieveConfig {
	return RetrieveConfig{Limit: 5}
}

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

// NewMemorySlideHandler creates a SlideHandler that stores slid context window
// messages as MemoryRecords. This forms the write-half of the memory closed loop:
//
//	doSlide() evicts old messages → SlideEvent
//	This handler stores each message as a MemoryRecord → SessionRAG
//	buildCallInput retrieves relevant records → prompts next-round recall
//
// The handler stores messages with tags ["slided", "context", "<role>"] so
// that retrievers (e.g. MindX's SessionRAG) can filter or weight appropriately.
//
// Example:
//
//	memory := myMemoryImpl(...)
//	slideHandler := core.NewMemorySlideHandler(memory)
//	llmCaller := NewLLMCaller(cfg, client, estimator, store,
//	    WithLLMCallerSlideHandler(slideHandler),
//	)
func NewMemorySlideHandler(memory Memory) SlideHandler {
	return func(ctx context.Context, event SlideEvent) {
		for i, msg := range event.Slided {
			record := MemoryRecord{
				ID:        fmt.Sprintf("slide-%s-%d-%d", event.SessionID, event.Timestamp, i),
				SessionID: event.SessionID,
				Type:      MemoryTypeSession,
				Title:     msg.Role + " message",
				Content:   fmt.Sprintf("[%s]\n%s", msg.Role, msg.Content),
				Tags:      []string{"slided", "context", msg.Role},
				CreatedAt: time.Unix(event.Timestamp, 0),
			}
			if _, err := memory.Store(ctx, record); err != nil {
				slog.Warn("memory slide handler: failed to store slided message",
					"session_id", event.SessionID,
					"error", err,
					"role", msg.Role,
				)
			}
		}
	}
}

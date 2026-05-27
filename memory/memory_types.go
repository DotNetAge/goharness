package memory

import (
	"context"
	"errors"
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
	MemoryTypeSession MemoryType = iota
	MemoryTypeLongTerm
)

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

type Memory interface {
	Retrieve(ctx context.Context, query string, opts ...RetrieveOption) ([]MemoryRecord, error)
	Store(ctx context.Context, record MemoryRecord) (string, error)
	Delete(ctx context.Context, id string) error
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


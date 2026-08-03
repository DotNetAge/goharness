package session

import (
	"context"
	"sync"

	"github.com/DotNetAge/goharness/memory"
)

// MemoryStore defines the interface for storing and retrieving session memory/compaction chunks.
// Implementations can use various backends (Redis, database, RAGMemory, etc.)
//
// StoreChunks is called during compaction to persist compaction chunks as MemoryChunks.
// Retrieve can be used to load historical context when needed.
type MemoryStore interface {
	// StoreChunks persists memory chunks associated with a session.
	StoreChunks(ctx context.Context, sessionID string, chunks []memory.MemoryChunk) error

	// Retrieve loads memory chunks for a session, returning up to `limit` most recent entries.
	Retrieve(ctx context.Context, query, sessionID string, limit int) ([]memory.MemoryChunk, error)
}

// inMemoryMemory provides an in-memory implementation of MemoryStore for development
// and testing purposes.
type inMemoryMemory struct {
	mu   sync.RWMutex
	data map[string][]memory.MemoryChunk
}

// newInMemoryMemory creates a new in-memory store initialized with an empty data map.
func newInMemoryMemory() *inMemoryMemory {
	return &inMemoryMemory{data: make(map[string][]memory.MemoryChunk)}
}

// StoreChunks saves memory chunks to the store under the given session ID.
// Chunks are appended to existing entries for the same session.
func (m *inMemoryMemory) StoreChunks(_ context.Context, sessionID string, chunks []memory.MemoryChunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = append(m.data[sessionID], chunks...)
	return nil
}

// Retrieve fetches stored memory chunks for a session, limited to the most recent `limit` entries.
func (m *inMemoryMemory) Retrieve(_ context.Context, query, sessionID string, limit int) ([]memory.MemoryChunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.data[sessionID]
	if len(all) == 0 {
		return nil, nil
	}
	if len(all) > limit {
		out := make([]memory.MemoryChunk, limit)
		copy(out, all[len(all)-limit:])
		return out, nil
	}
	out := make([]memory.MemoryChunk, len(all))
	copy(out, all)
	return out, nil
}

package session

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/memory"
)

type sessionMeta struct {
	role           string
	createdAt      time.Time
	lastActivityAt time.Time
}

type MemorySessionStore struct {
	mu          sync.RWMutex
	store       map[string][]Message
	metas       map[string]*sessionMeta
	cursors     map[string]int // cursor persistence for compaction state
	modifyFiles map[string][]string
	handler     SlideHandler
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		store:       make(map[string][]Message),
		metas:       make(map[string]*sessionMeta),
		cursors:     make(map[string]int),
		modifyFiles: make(map[string][]string),
		handler:     NoopSlideHandler,
	}
}

func (s *MemorySessionStore) Append(_ context.Context, sessionID string, agentName string, message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[sessionID] = append(s.store[sessionID], message)
	if meta, ok := s.metas[sessionID]; ok {
		meta.lastActivityAt = time.Now()
		if meta.role == "" && agentName != "" {
			meta.role = agentName
		}
	} else {
		s.metas[sessionID] = &sessionMeta{
			role:           agentName,
			lastActivityAt: time.Now(),
			createdAt:      time.Now(),
		}
	}
	return nil
}

func reverseMessages(msgs []Message) {
	for i, j := 0, len(msgs)-1; i < j; {
		msgs[i], msgs[j] = msgs[j], msgs[i]
		i++
		j--
	}
}

func (s *MemorySessionStore) Get(_ context.Context, sessionID string) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.store[sessionID]
	if msgs == nil {
		return nil, nil
	}
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

func (s *MemorySessionStore) CurrentContext(_ context.Context, agentName string, maxTokens int64) ([]Message, error) {
	s.mu.RLock()
	var targetSession string
	var bestTime time.Time
	for sid, meta := range s.metas {
		if meta.role == agentName && meta.lastActivityAt.After(bestTime) {
			targetSession = sid
			bestTime = meta.lastActivityAt
		}
	}
	if targetSession == "" {
		s.mu.RUnlock()
		return nil, nil
	}
	msgs := make([]Message, len(s.store[targetSession]))
	copy(msgs, s.store[targetSession])
	s.mu.RUnlock()
	if len(msgs) == 0 {
		return nil, nil
	}
	var selected []Message
	var usedTokens int64
	for i := len(msgs) - 1; i >= 0; i-- {
		msgTokens := int64(len(msgs[i].Content)/4 + 1)
		if usedTokens+msgTokens > maxTokens {
			break
		}
		selected = append(selected, msgs[i])
	}
	reverseMessages(selected)
	return selected, nil
}

func (s *MemorySessionStore) Delete(_ context.Context, timestamp int64, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.store[sessionID]
	filtered := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Timestamp != timestamp {
			filtered = append(filtered, m)
		}
	}
	s.store[sessionID] = filtered
	return nil
}

func (s *MemorySessionStore) Clear(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[sessionID] = nil
	return nil
}

func (s *MemorySessionStore) DeleteSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.store, sessionID)
	delete(s.metas, sessionID)
	delete(s.cursors, sessionID)
	delete(s.modifyFiles, sessionID)
	return nil
}

func (s *MemorySessionStore) SetSlideHandler(handler SlideHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = handler
}

func (s *MemorySessionStore) RegisterRole(sessionID, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if existing, ok := s.metas[sessionID]; ok {
		existing.role = role
	} else {
		s.metas[sessionID] = &sessionMeta{
			role:           role,
			createdAt:      now,
			lastActivityAt: now,
		}
	}
}

func (s *MemorySessionStore) GetByRole(_ context.Context, agentName string) (*SessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var bestID string
	var bestTime time.Time
	for sid, meta := range s.metas {
		if meta.role == agentName && meta.lastActivityAt.After(bestTime) {
			bestID = sid
			bestTime = meta.lastActivityAt
		}
	}
	if bestID == "" {
		return nil, ErrSessionNotFound
	}
	meta := s.metas[bestID]
	msgs := s.store[bestID]
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return &SessionInfo{
		SessionID:      bestID,
		AgentName:      meta.role,
		Messages:       out,
		LastActivityAt: meta.lastActivityAt,
		CreatedAt:      meta.createdAt,
	}, nil
}

func (s *MemorySessionStore) ListSessions(_ context.Context) ([]SessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]SessionInfo, 0, len(s.metas))
	for sid, meta := range s.metas {
		msgs := s.store[sid]
		out := make([]Message, len(msgs))
		copy(out, msgs)
		result = append(result, SessionInfo{
			SessionID:      sid,
			AgentName:      meta.role,
			Messages:       out,
			LastActivityAt: meta.lastActivityAt,
			CreatedAt:      meta.createdAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastActivityAt.After(result[j].LastActivityAt)
	})
	return result, nil
}

func (s *MemorySessionStore) Create(_ context.Context, agentName string, opts ...SessionOption) (*SessionInfo, error) {
	sessionID := fmt.Sprintf("sess_%d", time.Now().UnixNano()%100000000)
	sessionInfo := &SessionInfo{
		SessionID:      sessionID,
		AgentName:      agentName,
		CreatedAt:      time.Now(),
		LastActivityAt: time.Now(),
	}
	for _, opt := range opts {
		opt(sessionInfo)
	}
	if sessionInfo.ProjectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		sessionInfo.ProjectDir = cwd
	}
	s.mu.Lock()
	s.store[sessionID] = []Message{}
	s.metas[sessionID] = &sessionMeta{
		role:           agentName,
		createdAt:      time.Now(),
		lastActivityAt: time.Now(),
	}
	s.mu.Unlock()
	return sessionInfo, nil
}

func (s *MemorySessionStore) GetMeta(_ context.Context, sessionID string) (*SessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, exists := s.metas[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}
	msgs := s.store[sessionID]
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return &SessionInfo{
		SessionID:      sessionID,
		AgentName:      meta.role,
		Messages:       out,
		LastActivityAt: meta.lastActivityAt,
		CreatedAt:      meta.createdAt,
	}, nil
}

func (s *MemorySessionStore) ResolveSessionDir(sessionID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.metas[sessionID]; !exists {
		return "", ErrSessionNotFound
	}
	return "", nil
}
func (s *MemorySessionStore) Close() error {
	return nil
}

func (s *MemorySessionStore) GetCursor(_ context.Context, sessionID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cursor, ok := s.cursors[sessionID]; ok {
		return cursor, nil
	}
	return 0, nil // Default cursor is 0 (no compaction)
}

func (s *MemorySessionStore) SetCursor(_ context.Context, sessionID string, cursor int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[sessionID] = cursor
	return nil
}

func (s *MemorySessionStore) SaveModifyFiles(sessionID string, files []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if files == nil {
		s.modifyFiles[sessionID] = nil
	} else {
		copied := make([]string, len(files))
		copy(copied, files)
		s.modifyFiles[sessionID] = copied
	}
	return nil
}

func (s *MemorySessionStore) GetModifyFiles(sessionID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files, ok := s.modifyFiles[sessionID]
	if !ok || files == nil {
		return nil, nil
	}
	out := make([]string, len(files))
	copy(out, files)
	return out, nil
}

func (s *MemorySessionStore) Truncate(_ context.Context, sessionID string, keepCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs, ok := s.store[sessionID]
	if !ok {
		return nil
	}
	if keepCount >= len(msgs) {
		return nil
	}
	s.store[sessionID] = msgs[:keepCount]
	return nil
}

// MemoryStore defines the interface for storing and retrieving session memory/summaries.
// Implementations can use various backends (Redis, database, RAGMemory, etc.)
//
// StoreChunks is called during compaction to persist context summaries as MemoryChunks.
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

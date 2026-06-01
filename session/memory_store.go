package session

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

type sessionMeta struct {
	role           string
	createdAt      time.Time
	lastActivityAt time.Time
}

type MemorySessionStore struct {
	mu      sync.RWMutex
	store   map[string][]Message
	metas   map[string]*sessionMeta
	cursors map[string]int // cursor persistence for compaction state
	handler SlideHandler
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		store:   make(map[string][]Message),
		metas:   make(map[string]*sessionMeta),
		cursors: make(map[string]int),
		handler: NoopSlideHandler,
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
		usedTokens += msgTokens
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

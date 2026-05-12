package core

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// sessionMeta holds per-session metadata for role-based lookup.
type sessionMeta struct {
	role           string
	createdAt      time.Time
	lastActivityAt time.Time
}

// MemorySessionStore is an in-memory SessionStore implementation.
// Suitable for development, testing, and single-process deployments.
//
// It supports role-based session isolation: each session can be bound to an
// agent role so that Agent.Switch() resumes the most recent session for that
// role instead of creating a new one.
type MemorySessionStore struct {
	mu      sync.RWMutex
	store   map[string][]Message            // sessionID -> messages
	metas   map[string]*sessionMeta         // sessionID -> metadata (role, timestamps)
	usage   map[string][]TokenUsage          // sessionID -> token usage records
	handler SlideHandler
}

// NewMemorySessionStore creates a new empty in-memory session store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		store:   make(map[string][]Message),
		metas:   make(map[string]*sessionMeta),
		usage:   make(map[string][]TokenUsage),
		handler: NoopSlideHandler,
	}
}

func (s *MemorySessionStore) Append(_ context.Context, sessionID string, agentName string, message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[sessionID] = append(s.store[sessionID], message)

	// Update or create metadata with agent name binding
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
	// Find the most recent sessionID for this agent
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

	// Sliding-window: select from newest to oldest within token budget
	var selected []Message
	var usedTokens int64
	for i := len(msgs) - 1; i >= 0; i-- {
		msgTokens := int64(EstimateTokens(msgs[i].Content))
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

func (s *MemorySessionStore) SetSlideHandler(handler SlideHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = handler
}

func (s *MemorySessionStore) AppendTokenUsage(_ context.Context, sessionID string, usage TokenUsage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage[sessionID] = append(s.usage[sessionID], usage)
	return nil
}

func (s *MemorySessionStore) GetTokenUsages(_ context.Context, sessionID string) ([]TokenUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := s.usage[sessionID]
	if records == nil {
		return nil, nil
	}
	out := make([]TokenUsage, len(records))
	copy(out, records)
	return out, nil
}

func (s *MemorySessionStore) Close() error {
	return nil
}

// RegisterRole binds a session ID to an agent role.
// This should be called when a ContextWindow with a role is set on the Reactor.
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

// GetByRole returns the most recent SessionInfo for the given agent name.
// Returns ErrSessionNotFound if no session exists for that agent.
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

// ListSessions returns all sessions sorted by LastActivityAt descending (newest first).
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

// === Session Lifecycle Management (Framework-level directory control) ===

// Create creates a new in-memory session with the given agent name and options.
// For memory store, SessionDir is set to empty string (no filesystem backing).
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

// GetMeta returns complete session metadata including directory information.
// For memory store, this is equivalent to GetByRole but includes ProjectDir if set.
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

// ResolveSessionDir returns the filesystem path for the session's sandbox directory.
// For memory store, always returns empty string (no filesystem backing).
func (s *MemorySessionStore) ResolveSessionDir(sessionID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, exists := s.metas[sessionID]; !exists {
		return "", ErrSessionNotFound
	}

	return "", nil // Memory store has no filesystem path
}

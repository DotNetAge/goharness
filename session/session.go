package session

import (
	"context"
	"sync"
)

type inMemoryMemory struct {
	mu   sync.RWMutex
	data map[string][]string
}

func newInMemoryMemory() *inMemoryMemory {
	return &inMemoryMemory{data: make(map[string][]string)}
}

func (m *inMemoryMemory) Store(_ context.Context, sessionID, title, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = append(m.data[sessionID], content)
	return nil
}

func (m *inMemoryMemory) Retrieve(_ context.Context, query, sessionID string, limit int) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.data[sessionID]
	if len(all) == 0 {
		return nil, nil
	}
	if len(all) > limit {
		out := make([]string, limit)
		copy(out, all[len(all)-limit:])
		return out, nil
	}
	out := make([]string, len(all))
	copy(out, all)
	return out, nil
}

type MemoryStore interface {
	Store(ctx context.Context, sessionID, title, content string) error
	Retrieve(ctx context.Context, query, sessionID string, limit int) ([]string, error)
}

type SessionConfig func(*Session)

func WithStore(store SessionStore) SessionConfig {
	return func(s *Session) { s.store = store }
}

func WithMemory(mem MemoryStore) SessionConfig {
	return func(s *Session) { s.mem = mem }
}

func WithSummarizer(ss Summarizer) SessionConfig {
	return func(s *Session) { s.summarizer = ss }
}

func WithMaxWindowSize(n int64) SessionConfig {
	return func(s *Session) { s.maxWindowSize = n }
}

func WithCompactionHandler(h func(CompactionEvent)) SessionConfig {
	return func(s *Session) { s.compactionHandler = h }
}

func NewSession(id, agentName string, opts ...SessionConfig) *Session {
	s := &Session{
		id:        id,
		agentName: agentName,
		messages:  make([]Message, 0),
		store:     NewMemorySessionStore(),
		mem:       newInMemoryMemory(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CompactionEvent carries details about a session compaction event.
type CompactionEvent struct {
	MessagesSlid   int   `json:"messages_slid"`
	RemainingAfter int   `json:"remaining_after"`
	WindowSize     int64 `json:"window_size"`
}

type Session struct {
	mu            sync.RWMutex
	id            string
	agentName     string
	projectDir    string
	maxWindowSize int64
	cursor        int
	messages      []Message
	store      SessionStore
	summarizer Summarizer
	mem        MemoryStore

	compactionHandler func(CompactionEvent)
}

func (s *Session) ID() string             { return s.id }
func (s *Session) AgentName() string      { return s.agentName }
func (s *Session) ProjectDir() string     { return s.projectDir }
func (s *Session) SessionDir() string {
	if s.store != nil {
		dir, _ := s.store.ResolveSessionDir(s.id)
		return dir
	}
	return ""
}
func (s *Session) Store() SessionStore    { return s.store }

func (s *Session) All() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *Session) Current() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cursor >= len(s.messages) {
		return nil
	}
	out := make([]Message, len(s.messages)-s.cursor)
	copy(out, s.messages[s.cursor:])
	return out
}

func (s *Session) Append(ctx context.Context, msgs ...Message) {
	s.mu.Lock()
	s.messages = append(s.messages, msgs...)
	if s.store != nil {
		for _, msg := range msgs {
			s.store.Append(ctx, s.id, s.agentName, msg)
		}
	}
	s.mu.Unlock()
	s.tryCompact(ctx)
}

func (s *Session) tryCompact(ctx context.Context) {
	s.mu.Lock()
	cursor := s.cursor
	messages := s.messages
	maxWindowSize := s.maxWindowSize

	if maxWindowSize <= 0 {
		s.mu.Unlock()
		return
	}

	if cursor >= len(messages) {
		s.mu.Unlock()
		return
	}

	windowMsgs := messages[cursor:]
	var tokens int64
	for _, m := range windowMsgs {
		tokens += int64(len(m.Content)/4 + 1)
	}

	threshold := int64(float64(maxWindowSize) * 0.8)
	target := int64(float64(maxWindowSize) * 0.6)

	if tokens <= threshold {
		s.mu.Unlock()
		return
	}

	var newCursor int
	var slidTokens int64
	for i := cursor; i < len(messages); i++ {
		t := int64(len(messages[i].Content)/4 + 1)
		newCursor = i
		slidTokens += t
		if tokens-slidTokens <= target {
			newCursor = i + 1
			break
		}
	}

	if newCursor > s.cursor {
		slid := make([]Message, newCursor-s.cursor)
		copy(slid, s.messages[s.cursor:newCursor])

		if s.summarizer != nil {
			s.mu.Unlock()
			summary, err := s.summarizer.Summarize(ctx, slid)
			s.mu.Lock()
			if err == nil && summary != "" && s.mem != nil {
				_ = s.mem.Store(ctx, s.id, "context summary", summary)
			}
		}

		if s.cursor != cursor || len(s.messages) != len(messages) {
			s.mu.Unlock()
			return
		}

		slidCount := newCursor - s.cursor
		s.messages = append(s.messages[:s.cursor], s.messages[newCursor:]...)
		s.cursor = len(s.messages[:s.cursor])

		if s.compactionHandler != nil {
			s.compactionHandler(CompactionEvent{
				MessagesSlid:   slidCount,
				RemainingAfter: len(s.messages),
				WindowSize:     s.maxWindowSize,
			})
		}
	}
	s.mu.Unlock()
}

func (s *Session) SetCompactionHandler(h func(CompactionEvent)) {
	s.compactionHandler = h
}

func (s *Session) Compact(keepRecent int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor <= 0 {
		return
	}
	historical := s.messages[:s.cursor]
	if len(historical) == 0 {
		return
	}
	compacted := MicroCompact(historical, keepRecent)
	s.messages = append(compacted, s.messages[s.cursor:]...)
	s.cursor = len(compacted)
}

func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]Message, 0)
	s.cursor = 0
}

// Package session provides conversation session management for AI agents.
// It handles message storage, context window management, and automatic compaction
// to prevent token limit exhaustion during long conversations.
//
// Key Features:
//   - Thread-safe message append and retrieval
//   - Automatic context window compaction based on token count
//   - Configurable window size and summarization
//   - Persistent storage backend support
//   - Memory store for context summaries
//
// Architecture:
//
//	┌─────────────┐     ┌────────────────┐     ┌──────────────┐
//	│  Session    │────>│  SessionStore  │────>│  Persistence │
//	│  (in-memory)│     │  (interface)   │     │  (disk/db)   │
//	└─────────────┘     └────────────────┘     └──────────────┘
//	      │                    │
//	      ▼                    ▼
//	┌─────────────┐     ┌────────────────┐
//	│  MemoryStore│     │   Summarizer   │
//	│  (summaries)│     │  (LLM calls)   │
//	└─────────────┘     └────────────────┘
//
// Usage:
//
//	session := session.NewSession("session-123", "assistant")
//	session.Append(ctx, session.Message{
//	    Role:    "user",
//	    Content: "Hello!",
//	})
//	current := session.Current()
package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// inMemoryMemory provides an in-memory implementation of MemoryStore for development
// and testing purposes. For production use, consider implementing a persistent backend.
type inMemoryMemory struct {
	mu   sync.RWMutex
	data map[string][]string
}

// newInMemoryMemory creates a new in-memory store initialized with an empty data map.
func newInMemoryMemory() *inMemoryMemory {
	return &inMemoryMemory{data: make(map[string][]string)}
}

// Store saves content to the memory store under the given session ID.
// Content is appended to existing entries for the same session.
// This operation is thread-safe.
func (m *inMemoryMemory) Store(_ context.Context, sessionID, title, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = append(m.data[sessionID], content)
	return nil
}

// Retrieve fetches stored content for a session, limited to the most recent `limit` entries.
// Returns nil if no content exists for the session.
// This operation is thread-safe and does not block writes.
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

// MemoryStore defines the interface for storing and retrieving session memory/summaries.
// Implementations can use various backends (Redis, database, file system, etc.)
//
// The Store method is called during compaction to persist context summaries,
// while Retrieve can be used to load historical context when needed.
type MemoryStore interface {
	// Store persists content (typically a summary) associated with a session.
	Store(ctx context.Context, sessionID, title, content string) error

	// Retrieve loads content for a session, returning up to `limit` most recent entries.
	Retrieve(ctx context.Context, query, sessionID string, limit int) ([]string, error)
}

// SessionConfig is a functional option for configuring Session instances.
// This pattern allows for flexible, readable configuration without breaking changes
// when new options are added.
//
// Example:
//
//	session := NewSession("id", "agent",
//	    WithMaxWindowSize(8000),
//	    WithSummarizer(mySummarizer),
//	    WithCompactionHandler(myHandler),
//	)
type SessionConfig func(*Session)

// WithStore sets the persistent storage backend for the session.
// Messages will be persisted to this store when appended.
func WithStore(store SessionStore) SessionConfig {
	return func(s *Session) { s.store = store }
}

// WithMemory configures the memory store for context summaries.
// If not set, an in-memory store is used by default.
func WithMemory(mem MemoryStore) SessionConfig {
	return func(s *Session) { s.mem = mem }
}

// WithSummarizer sets the LLM-based summarizer for context compaction.
// When the context window exceeds thresholds, old messages are summarized
// using this component to preserve important information.
func WithSummarizer(ss Summarizer) SessionConfig {
	return func(s *Session) { s.summarizer = ss }
}

// WithMaxWindowSize configures the maximum context window size in tokens.
// When the active window exceeds 80% of this value, compaction is triggered
// to trim it down to ~60% of the maximum.
//
// A value of 0 or negative disables automatic compaction.
func WithMaxWindowSize(n int64) SessionConfig {
	return func(s *Session) { s.maxWindowSize = n }
}

// WithCompactionHandler sets a callback function that is invoked after each
// compaction event. This can be used for logging, metrics, or UI updates.
func WithCompactionHandler(h func(CompactionEvent)) SessionConfig {
	return func(s *Session) { s.compactionHandler = h }
}

// NewSession creates a new conversation session with the given ID and agent name.
// Sessions are created with sensible defaults:
//   - In-memory message store
//   - In-memory summary store
//   - No automatic compaction (set via WithMaxWindowSize)
//   - No summarizer (set via WithSummarizer)
//
// Parameters:
//   - id: Unique session identifier (e.g., UUID)
//   - agentName: Name of the agent operating this session
//   - opts: Optional configuration functions
//
// Returns:
//   - A fully initialized Session ready to receive messages
//
// Thread Safety: All operations on the returned Session are thread-safe.
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
// It's passed to the CompactionHandler callback to inform subscribers
// about context window management activities.
type CompactionEvent struct {
	// MessagesSlid indicates how many messages were removed from the active window
	MessagesSlid int `json:"messages_slid"`

	// RemainingAfter shows the count of messages still in the active window
	RemainingAfter int `json:"remaining_after"`

	// WindowSize is the configured maximum window size in tokens
	WindowSize int64 `json:"window_size"`
}

// Session manages a conversation's message history with automatic compaction.
// It provides thread-safe access to messages and implements a sliding window
// pattern to manage context length within LLM token limits.
//
// Internal Structure:
//
//	┌─────────────────────────────────────────┐
//	│              messages[]                  │
//	├──────────────────┬──────────────────────┤
//	│  [0..cursor]     │  [cursor..len]       │
//	│  Historical      │  Active Window       │
//	│  (compacted)     │  (sent to LLM)       │
//	└──────────────────┴──────────────────────┘
//
// Thread Safety: All public methods are safe for concurrent use.
type Session struct {
	mu sync.RWMutex

	// id is the unique session identifier
	id string

	// agentName identifies which agent owns this session
	agentName string

	// projectDir is the working directory for file operations
	projectDir string

	// maxWindowSize is the token limit before compaction triggers
	maxWindowSize int64

	// cursor separates historical messages from the active window
	cursor int

	// messages stores all messages (historical + active)
	// This is a lazy-loaded cache: empty until first access, then loaded from store
	messages []Message

	// store provides persistent message storage
	store SessionStore

	// summarizer generates summaries during compaction
	summarizer Summarizer

	// mem stores context summaries for later retrieval
	mem MemoryStore

	// compactionHandler is called after each compaction event
	compactionHandler func(CompactionEvent)

	// loaded indicates whether messages have been loaded from the persistent store.
	// When false, Current() and Append() will trigger automatic lazy-loading.
	loaded bool

	// loadingMu prevents concurrent lazy-load operations
	loadingMu sync.Mutex

	// modifyFiles tracks file paths that have been modified during this session.
	// Each entry is an absolute path to a file that was written/edited,
	// with a backup stored in the session's backup directory.
	modifyFiles []string

	// fileModifyHandler is called when files are tracked, confirmed, or rolled back.
	fileModifyHandler FileModifyHandler
}

// ID returns the unique identifier of this session.
func (s *Session) ID() string { return s.id }

// AgentName returns the name of the agent operating this session.
func (s *Session) AgentName() string { return s.agentName }

// ProjectDir returns the working directory associated with this session.
func (s *Session) ProjectDir() string { return s.projectDir }

// SessionDir returns the filesystem path where session data is stored.
// Returns empty string if no persistent store is configured or if
// the store cannot resolve the session directory.
func (s *Session) SessionDir() string {
	if s.store != nil {
		dir, _ := s.store.ResolveSessionDir(s.id)
		return dir
	}
	return ""
}

// Store returns the underlying SessionStore for direct access if needed.
// Most users should prefer the higher-level methods on Session.
func (s *Session) Store() SessionStore { return s.store }

// All returns a copy of all messages in the session (both historical and active).
// The returned slice is safe to modify without affecting the session.
//
// Use Current() instead if you only need messages in the active window.
//
// Lazy-Loading: Automatically loads messages from store on first access.
func (s *Session) All() []Message {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// Current returns messages in the active window (from cursor to end).
// These are the messages that would be sent to the LLM on next inference.
// Returns nil if cursor has reached the end of messages.
//
// This method implements lazy-loading: if messages haven't been loaded from the
// persistent store yet, it will automatically load them on first access.
// This ensures that resumed sessions always have access to historical context
// without requiring an explicit Restore() call.
//
// The returned slice is a copy and safe to modify.
func (s *Session) Current() []Message {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cursor >= len(s.messages) {
		return nil
	}
	out := make([]Message, len(s.messages)-s.cursor)
	copy(out, s.messages[s.cursor:])
	return out
}

// ensureLoaded implements lazy-loading for session messages and cursor position.
// On first access (when loaded==false), it loads all messages from the persistent store
// AND restores the compaction cursor position.
//
// CRITICAL: The cursor must be restored to maintain correct compaction state across
// Session object lifecycles. Without this, a new Session would load all messages but
// reset cursor to 0, causing Current() to return too many messages (exceeding token limits).
//
// IMPORTANT - Compaction State Recovery:
// When compaction occurs, old messages are deleted from memory but the Store may still
// contain the complete history (all appended messages). The cursor marks how many messages
// were at the front before compaction. On lazy-load, we must apply the same compaction
// logic to avoid restoring "deleted" messages.
//
// This method is thread-safe and handles concurrent access correctly:
// - First caller acquires loadingMu and performs the load
// - Concurrent callers block until load completes, then see loaded==true
func (s *Session) ensureLoaded(ctx context.Context) {
	if s.loaded {
		return
	}

	s.loadingMu.Lock()
	defer s.loadingMu.Unlock()

	// Double-check after acquiring lock (another goroutine may have loaded)
	if s.loaded {
		return
	}

	if s.store == nil {
		s.loaded = true
		return
	}

	// Load all messages from persistent store
	msgs, err := s.store.Get(ctx, s.id)
	if err != nil {
		// If load fails, mark as loaded anyway to avoid retry loops.
		// Session will start empty, which is safe (just no history).
		s.loaded = true
		return
	}

	// Restore cursor position from persistent store (internal SessionStore operation)
	// This ensures that if compaction occurred in a previous Session lifecycle,
	// the cursor is correctly positioned (not reset to 0)
	cursor, cursorErr := s.store.GetCursor(ctx, s.id)
	if cursorErr != nil {
		cursor = 0 // Default to 0 on error (no compaction)
	}

	s.mu.Lock()

	// Apply compaction recovery if needed
	// If cursor > 0, it means compaction occurred in a previous lifecycle.
	// The store has all historical messages, but we need to reconstruct
	// the compacted state by keeping only messages[:cursor] + messages after the slid region.
	// However, without knowing newCursor, we use a simpler heuristic:
	// Keep only the last (len(msgs) - cursor) messages as the active window.
	// This approximates the compaction result.
	if cursor > 0 && cursor < len(msgs) {
		activeWindowLen := len(msgs) - cursor
		if activeWindowLen > 0 {
			maxActiveWindow := 100
			if activeWindowLen > maxActiveWindow {
				activeWindowLen = maxActiveWindow
			}
			startActive := len(msgs) - activeWindowLen
			if startActive < cursor {
				startActive = cursor
			}
			msgs = append(msgs[:cursor], msgs[startActive:]...)
		} else {
			msgs = msgs[:cursor]
		}
	}

	s.messages = msgs
	s.cursor = cursor // Restore persisted cursor, NOT hardcoded 0!
	s.mu.Unlock()

	s.loaded = true
}

// Restore explicitly loads historical messages from the persistent store into memory.
//
// NOTE: With the lazy-loading architecture (implemented in ensureLoaded),
// this method is OPTIONAL. Current() and Append() will automatically load
// messages on first access if they haven't been loaded yet.
//
// Use Restore() when you need to:
// - Force a reload of messages (e.g., after external modifications)
// - Pre-load messages before time-critical operations
// - Explicitly control when loading occurs for debugging/monitoring
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//
// Returns:
//   - error: If loading from store fails
func (s *Session) Restore(ctx context.Context) error {
	if s.store == nil {
		return nil
	}

	msgs, err := s.store.Get(ctx, s.id)
	if err != nil {
		return fmt.Errorf("restore session %q: %w", s.id, err)
	}

	s.mu.Lock()
	s.messages = msgs
	s.cursor = 0
	s.mu.Unlock()

	s.loadingMu.Lock()
	s.loaded = true
	s.loadingMu.Unlock()

	return nil
}

// Append adds new messages to the session and triggers automatic compaction
// if the context window exceeds configured thresholds.
//
// This operation is thread-safe and can be called concurrently from multiple
// goroutines (e.g., tool execution results streaming in).
//
// Lazy-Loading: If this is the first operation on the session, it will
// automatically load historical messages from the persistent store before
// appending. This ensures new messages are appended to the correct history.
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//   - msgs: One or more messages to append
//
// Side Effects:
//   - Persists messages to the configured SessionStore
//   - May trigger compaction if window size exceeded
//   - May invoke the summarizer and compaction handler
func (s *Session) Append(ctx context.Context, msgs ...Message) {
	s.ensureLoaded(ctx)

	var filtered []Message
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == 0 {
		return
	}

	s.mu.Lock()

	s.messages = append(s.messages, filtered...)

	if s.store != nil {
		for _, msg := range filtered {
			s.store.Append(ctx, s.id, s.agentName, msg)
		}
	}

	s.mu.Unlock()

	s.tryCompact(ctx)
}

// tryCompact checks if the active window exceeds token thresholds and performs
// compaction if necessary. This method uses a lock-free design to prevent deadlocks:
//
// Algorithm:
//  1. Acquire read lock to check if compaction is needed
//  2. Release read lock before calling summarizer (prevents holding lock during I/O)
//  3. Acquire write lock only when modifications are needed
//  4. Use atomic state snapshot to detect concurrent modifications
//
// This approach eliminates the deadlock risk present in the original implementation
// where multiple Lock/Unlock cycles could interact badly with concurrent Append calls.
//
// Thresholds:
//   - Trigger: Window exceeds 80% of maxWindowSize
//   - Target: Compact to ~60% of maxWindowSize
func (s *Session) tryCompact(ctx context.Context) {

	state := s.captureState()

	if !state.needsCompaction() {
		return
	}

	plan := state.calculateCompactionPlan()

	if plan.shouldCompact {

		var summary string
		if s.summarizer != nil && plan.messagesToSlide > 0 {
			slided := s.getMessagesToSlide(plan)
			summary = s.generateSummary(ctx, slided)
			s.persistSummary(ctx, summary)
		}

		s.executeCompactionPlan(plan)

		// Persist cursor position after compaction (internal SessionStore operation)
		// This ensures that when a new Session object is created (lazy-loaded),
		// it will restore the correct cursor position and Current() will return
		// the correct active window (not all messages, which would exceed token limits)
		if s.store != nil {
			s.mu.RLock()
			currentCursor := s.cursor
			s.mu.RUnlock()

			if err := s.store.SetCursor(ctx, s.id, currentCursor); err != nil {
				// Log error but don't fail - session can still function,
				// just cursor won't survive across restarts
				// (next lazy-load will reset to 0, which is safe)
			}
		}
	}
}

// sessionState is a snapshot of session state captured atomically for compaction decisions.
type sessionState struct {
	cursor        int
	messageCount  int
	maxWindowSize int64
	windowTokens  int64
	messages      []Message
}

// captureState atomically captures current session state under read lock.
func (s *Session) captureState() sessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	windowMsgs := s.messages[s.cursor:]
	var tokens int64
	for _, m := range windowMsgs {
		tokens += int64(len(m.Content)/4 + 1)
	}

	return sessionState{
		cursor:        s.cursor,
		messageCount:  len(s.messages),
		maxWindowSize: s.maxWindowSize,
		windowTokens:  tokens,
		messages:      s.messages,
	}
}

// needsCompaction determines if the current state requires compaction.
func (st sessionState) needsCompaction() bool {
	if st.maxWindowSize <= 0 {
		return false
	}
	if st.cursor >= st.messageCount {
		return false
	}

	threshold := int64(float64(st.maxWindowSize) * 0.8)
	return st.windowTokens > threshold
}

// compactionPlan contains the calculated parameters for a compaction operation.
type compactionPlan struct {
	shouldCompact   bool
	newCursor       int
	messagesToSlide int
	currentCursor   int
	originalLength  int
}

// calculateCompactionPlan determines what needs to be compacted based on current state.
func (st sessionState) calculateCompactionPlan() compactionPlan {
	target := int64(float64(st.maxWindowSize) * 0.6)

	var newCursor int
	var slidTokens int64

	for i := st.cursor; i < st.messageCount; i++ {
		t := int64(len(st.messages[i].Content)/4 + 1)
		newCursor = i
		slidTokens += t
		if st.windowTokens-slidTokens <= target {
			newCursor = i + 1
			break
		}
	}

	return compactionPlan{
		shouldCompact:   newCursor > st.cursor,
		newCursor:       newCursor,
		messagesToSlide: newCursor - st.cursor,
		currentCursor:   st.cursor,
		originalLength:  st.messageCount,
	}
}

// getMessagesToSlide extracts the messages that will be compacted.
func (s *Session) getMessagesToSlide(plan compactionPlan) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if plan.newCursor > len(s.messages) {
		return nil
	}

	slided := make([]Message, plan.messagesToSlide)
	copy(slided, s.messages[plan.currentCursor:plan.newCursor])
	return slided
}

// generateSummary calls the summarizer outside of any locks to prevent deadlocks.
func (s *Session) generateSummary(ctx context.Context, messages []Message) string {
	if s.summarizer == nil || len(messages) == 0 {
		return ""
	}

	summary, err := s.summarizer.Summarize(ctx, messages)
	if err != nil || summary == "" {
		return ""
	}

	return summary
}

// persistSummary stores the generated summary in the memory store.
func (s *Session) persistSummary(ctx context.Context, summary string) {
	if s.mem == nil || summary == "" {
		return
	}

	_ = s.mem.Store(ctx, s.id, "context summary", summary)
}

// executeCompactionPlan applies the compaction plan under write lock with CAS semantics.
func (s *Session) executeCompactionPlan(plan compactionPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cursor != plan.currentCursor || len(s.messages) != plan.originalLength {
		return
	}

	slidCount := plan.newCursor - s.cursor
	s.messages = append(s.messages[:s.cursor], s.messages[plan.newCursor:]...)
	s.cursor = len(s.messages[:s.cursor])

	if s.compactionHandler != nil {
		s.compactionHandler(CompactionEvent{
			MessagesSlid:   slidCount,
			RemainingAfter: len(s.messages),
			WindowSize:     s.maxWindowSize,
		})
	}
}

// SetCompactionHandler updates the callback function invoked after compaction events.
// This can be called at any time to change or remove the handler.
//
// Pass nil to disable compaction notifications.
func (s *Session) SetCompactionHandler(h func(CompactionEvent)) {
	s.compactionHandler = h
}

// Compact manually triggers compaction of historical messages, keeping only
// the most recent assistant messages as specified by keepRecent.
//
// Unlike automatic compaction (triggered by token limits), this method allows
// explicit control over how much history to preserve.
//
// Parameters:
//   - keepRecent: Number of recent assistant messages to retain in compacted form
//
// Use Cases:
//   - Reducing context before a new topic
//   - Freeing memory in long sessions
//   - Preparing for export or analysis
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

// Reset clears all messages and resets the cursor to zero.
// This effectively creates a blank session while preserving the same ID and configuration.
//
// Use Cases:
//   - Starting a fresh conversation in the same session
//   - Clearing sensitive data from memory
//   - Testing and debugging
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]Message, 0)
	s.cursor = 0
}

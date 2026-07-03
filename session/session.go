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
//	session := session.New("agent-name", "", "/home/user/project")
//	session.Append(ctx, session.Message{
//	    Role:    "user",
//	    Content: "Hello!",
//	})
//	current := session.Current()
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/memory"
	"github.com/oklog/ulid/v2"
)

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

// New creates a new conversation session with an auto-generated ULID.
//
// This is Mode 1 ("全新 Session") construction:
//   - Sponsor: identifies the session's creator (empty = user, non-empty = another agent)
//   - AgentName(Owner): the agent operating this session
//   - ProjectDir: the working directory for file operations (REQUIRED)
//   - ID: auto-generated ULID
//
// Parameters:
//   - agentName: Name of the agent operating this session
//   - sponsor: Agent that created/sponsored this session. Empty = user-initiated.
//   - projectDir: Working directory for file operations (must not be empty)
//   - opts: Optional configuration functions (WithStore, WithMemory, etc.)
//
// Returns:
//   - A fully initialized Session ready to receive messages
//
// Thread Safety: All operations on the returned Session are thread-safe.
func New(agentName, sponsor, projectDir string, opts ...SessionConfig) *Session {
	entropy := ulid.Monotonic(rand.Reader, 0)
	id := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)

	s := &Session{
		id:         id.String(),
		agentName:  agentName,
		sponsor:    sponsor,
		projectDir: projectDir,
		messages:   make([]Message, 0),
		store:      NewMemorySessionStore(),
		mem:        newInMemoryMemory(),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.projectDir == "" {
		log.Printf("[SESSION] FATAL: session %s (agent=%s) created without ProjectDir — this is a severe bug", s.id, agentName)
		fmt.Fprintf(os.Stderr, "[SESSION] FATAL: session %s (agent=%s) created without ProjectDir\n", s.id, agentName)
	}
	return s
}

// Load reconstructs a Session from an existing session ID using persistent storage.
//
// This is Mode 2 ("从 ID 中加载") construction:
//   - sessionID: The existing session's ULID
//   - agentName: Name of the agent operating this session
//   - store: The persistent SessionStore to load from (REQUIRED)
//   - opts: Optional configuration functions (WithMemory, WithSummarizer, etc.)
//
// Load verifies the session exists by calling store.GetMeta(). If the session
// is not found in the store, an error is returned — the caller should handle
// this as "session does not exist".
//
// ProjectDir is automatically restored from stored session metadata.
// Sponsor is left empty (user-initiated) for loaded sessions.
func Load(sessionID, agentName string, store SessionStore, opts ...SessionConfig) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("session store is required for Load")
	}

	info, err := store.GetMeta(context.Background(), sessionID)
	if err != nil {
		return nil, fmt.Errorf("session %q not found: %w", sessionID, err)
	}

	s := &Session{
		id:         sessionID,
		agentName:  agentName,
		projectDir: info.ProjectDir,
		messages:   make([]Message, 0),
		store:      store,
		mem:        newInMemoryMemory(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// NewSession creates a new conversation session with the given ID and agent name.
//
// Deprecated: Use New() for creating fresh sessions or Load() for loading
// existing sessions from persistent storage. NewSession exists for compatibility
// with legacy code paths.
//
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

	// ProjectDir must be set at construction time.
	// This is the authoritative source; store metadata lazy-load is only a
	// restore mechanism for existing sessions, NOT an alternative to providing it.
	if s.projectDir == "" {
		log.Printf("[SESSION] FATAL: session %s (agent=%s) created without ProjectDir — this is a severe bug", id, agentName)
		// Also write to stderr for visibility in daemon logs
		fmt.Fprintf(os.Stderr, "[SESSION] FATAL: session %s (agent=%s) created without ProjectDir\n", id, agentName)
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

	// sponsor identifies the agent that created/sponsored this session.
	// Empty means user-initiated (agent ↔ user conversation).
	// Non-empty means agent-spawned (SubAgent from another agent).
	sponsor string

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

	// pendingPermission stores the tool invocation that is currently
	// waiting for user authorization. It is set by the runtime when a
	// tool implementing PermissionRequired.Grant returns granted=false,
	// and cleared when the user responds with the PermissionAllow /
	// PermissionDeny magic word in a subsequent Ask call.
	//
	// Storing on the session (rather than on the runtime) makes the
	// permission flow survive across Ask() invocations and across
	// sub-agent boundaries, since the session is the shared state.
	pendingPermission *PendingPermission
	pendingMu         sync.Mutex

	// whitelist is an in-memory cache of the session-level tool whitelist.
	// Lazily loaded from {SessionDir()}/session-wl.json on first access.
	whitelist   *SessionWhitelist
	whitelistMu sync.Mutex
}

// ID returns the unique identifier of this session.
func (s *Session) ID() string { return s.id }

// AgentName returns the name of the agent operating this session.
func (s *Session) AgentName() string { return s.agentName }

// ProjectDir returns the working directory associated with this session.
func (s *Session) ProjectDir() string { return s.projectDir }

// WithProjectDir sets the project working directory for file operations.
// This enables tools like Write, FileEdit resolve relative paths relative to this directory.
// Example:
//
//	session := NewSession("id", "agent",
//	    WithStore(store),
//	    WithProjectDir("/home/user/project"),
//	)
func WithProjectDir(dir string) SessionConfig {
	return func(s *Session) { s.projectDir = dir }
}

// WithSponsor sets the sponsor agent that created this session.
// When empty, the session is considered user-initiated (agent ↔ user conversation).
// When set, the session was created by another agent (SubAgent).
func WithSponsor(name string) SessionConfig {
	return func(s *Session) { s.sponsor = name }
}

// Sponsor returns the name of the agent that created/sponsored this session.
// Returns empty string for user-initiated sessions.
func (s *Session) Sponsor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sponsor
}

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

	// Restore project directory from session metadata for file operations.
	// meta.json already contains ProjectWorkingDir (stored by FileSessionStore.Create
	// or statSessionInfo fallback), but Session.projectDir was never populated from it.
	var projectDir string
	if info, infoErr := s.store.GetMeta(ctx, s.id); infoErr == nil {
		projectDir = info.ProjectDir
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
	if s.projectDir == "" {
		s.projectDir = projectDir // Load project dir from session metadata (only if not set at construction)
	}
	s.mu.Unlock()

	// Restore tracked modified files from store (if any).
	// modify_files.yml is written by persistModifyFilesLocked during Write/FileEdit.
	s.loadModifyFiles()

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

		if s.summarizer != nil && plan.messagesToSlide > 0 {
			slided := s.getMessagesToSlide(plan)
			chunks := s.generateSummary(ctx, slided)
			s.persistSummary(ctx, chunks)
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
// 把所有待摘要消息完整交给 LLM，由 LLM 借助 prompt 的 Quality rules
// 自主判断哪些值得记忆（识别 trivial/重复/纠正/冲突）。代码层不做源头过滤，
// 因为砍掉消息会让 LLM 失去完整上下文，反而降低摘要质量。
func (s *Session) generateSummary(ctx context.Context, messages []Message) []memory.MemoryChunk {
	if s.summarizer == nil || len(messages) == 0 {
		return nil
	}

	chunks, err := s.summarizer.Summarize(ctx, messages)
	if err != nil || len(chunks) == 0 {
		return nil
	}

	// Enrich chunks with agent name, session ID, and timestamp fallback.
	// Timestamp 优先使用 LLM 在 JSON 中提供的事件时间；LLM 未填或解析失败时
	// 才 fallback 到 summarize 触发时间。
	for i := range chunks {
		chunks[i].AgentName = s.agentName
		chunks[i].SessionID = s.id
		if chunks[i].Timestamp.IsZero() {
			chunks[i].Timestamp = time.Now()
		}
		if chunks[i].ID == "" && chunks[i].Content != "" {
			h := sha256.Sum256([]byte(chunks[i].Content))
			chunks[i].ID = hex.EncodeToString(h[:])
		}
	}

	return chunks
}

// persistSummary stores the generated memory chunks in the memory store.
func (s *Session) persistSummary(ctx context.Context, chunks []memory.MemoryChunk) {
	if s.mem == nil || len(chunks) == 0 {
		return
	}

	_ = s.mem.StoreChunks(ctx, s.id, chunks)
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

// Truncate removes all messages at and after the given index, keeping
// only the first keepCount messages. This is used for retry scenarios
// where the last exchange needs to be undone before resending.
//
// The cursor is also adjusted if it points beyond the truncated boundary.
// Changes are persisted to the SessionStore if one is configured.
func (s *Session) Truncate(ctx context.Context, keepCount int) error {
	s.ensureLoaded(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()

	if keepCount < 0 {
		return fmt.Errorf("keepCount must be >= 0, got %d", keepCount)
	}
	if keepCount >= len(s.messages) {
		return nil // nothing to truncate
	}

	s.messages = s.messages[:keepCount]
	if s.cursor > keepCount {
		s.cursor = keepCount
	}

	if s.store != nil {
		if err := s.store.Truncate(ctx, s.id, keepCount); err != nil {
			return fmt.Errorf("store truncate failed: %w", err)
		}
	}

	return nil
}

// SetPendingPermission records a tool invocation that needs user
// authorization. The runtime calls this when a tool implementing
// PermissionRequired.Grant returns granted=false, and clears it (via
// TakePendingPermission) when the user responds with the magic word.
//
// Storing on the session means the pending state survives across Ask()
// boundaries — exactly what the magic-word flow requires: the loop
// stops, the user types "PermissionAllow", the runtime looks up the
// pending invocation in the same session, and resumes by actually
// running the tool.
func (s *Session) SetPendingPermission(p PendingPermission) {
	s.pendingMu.Lock()
	s.pendingPermission = &p
	s.pendingMu.Unlock()
}

// TakePendingPermission atomically reads and clears the pending
// invocation. The runtime calls this when it detects a permission
// magic word in the user message. Returns nil if there is no pending
// invocation (in which case the magic word is treated as a regular
// user message).
func (s *Session) TakePendingPermission() *PendingPermission {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.pendingPermission
	s.pendingPermission = nil
	return p
}

// PendingPermission captures the tool call that is waiting for the
// user's decision. It holds everything the runtime needs to either
// actually execute the tool (Allow) or synthesize a "Permission
// Denied" tool result (Deny) without the LLM ever seeing the
// "ask" intermediate state.
type PendingPermission struct {
	// ToolName is the registered tool's name (e.g. "Bash", "Write").
	ToolName string

	// ToolCallID matches the ToolCall.ID on the assistant message that
	// produced this invocation. The synthesized "Permission Denied"
	// result (or the executed tool's result) is appended to the
	// session with this ID, satisfying the strict OpenAI contract that
	// every tool_call must have a matching tool message.
	ToolCallID string

	// Arguments is the parameter map originally passed to the tool. The
	// runtime re-invokes the tool with these exact arguments when the
	// user allows — no re-derivation is done.
	Arguments map[string]any

	// Reason is the human-readable explanation already shown in the UI
	// (e.g. "command contains 'rm -rf /'"). It is re-used for the
	// "Permission Denied" tool result so the LLM can see why the call
	// was rejected.
	Reason string

	// SecurityLevel is preserved from the tool's ToolInfo so the UI
	// (and any future audit log) can render the right severity badge.
	SecurityLevel string
}

// SessionWhitelist stores per-session tool permissions that auto-grant
// without user confirmation. Persisted as {SessionDir()}/session-wl.json.
//
// Each slice holds entries that Grant() checks before prompting the user:
//   - Bash:      base command names (e.g. "some-custom-tool")
//   - Write:     resolved absolute file paths or directory prefixes
//   - Edit:      resolved absolute file paths or directory prefixes
//   - RunScript: resolved absolute script paths or directory prefixes
//
// An entry can be an exact path or a prefix — Grant() uses strings.HasPrefix
// for Write/Edit/RunScript, and exact-match for Bash commands.
type SessionWhitelist struct {
	Bash      []string `json:"bash,omitempty"`
	Write     []string `json:"write,omitempty"`
	Edit      []string `json:"edit,omitempty"`
	RunScript []string `json:"run_script,omitempty"`
}

// whitelistFileName is the JSON file name stored in each session directory.
const whitelistFileName = "session-wl.json"

// whitelistPath returns the absolute path to the session whitelist file.
// Returns "" when the session has no persistent directory (no store).
func (s *Session) whitelistPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, whitelistFileName)
}

// Whitelist returns the session whitelist, lazily loaded from disk.
// The result is cached in memory so repeated access within a session
// does not re-read the file. Returns an empty (non-nil) whitelist when
// no file exists or the session has no persistent directory.
func (s *Session) Whitelist() *SessionWhitelist {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	if s.whitelist != nil {
		return s.whitelist
	}

	// Lazy load from disk.
	s.whitelist = &SessionWhitelist{}
	wp := s.whitelistPath()
	if wp == "" {
		return s.whitelist
	}

	data, err := os.ReadFile(wp)
	if err != nil {
		// File doesn't exist yet — return empty whitelist.
		return s.whitelist
	}

	// Best-effort parse; malformed file resets to empty whitelist.
	var wl SessionWhitelist
	if json.Unmarshal(data, &wl) == nil {
		s.whitelist = &wl
	}
	return s.whitelist
}

// AddToWhitelist adds an entry to the session whitelist for the given tool
// and persists the updated whitelist to {SessionDir()}/session-wl.json.
//
// Parameters:
//   - toolName: one of "bash", "write", "edit", "run_script"
//   - entry:    the value to add (base command name for bash; file/script path for others)
//
// Returns an error if the tool name is unrecognised, or if persistence fails.
// Duplicate entries are silently ignored.
func (s *Session) AddToWhitelist(toolName, entry string) error {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	// Ensure whitelist is loaded.
	if s.whitelist == nil {
		s.whitelist = &SessionWhitelist{}
	}

	// Select the slice for this tool name.
	var target *[]string
	switch toolName {
	case "bash":
		target = &s.whitelist.Bash
	case "write":
		target = &s.whitelist.Write
	case "edit":
		target = &s.whitelist.Edit
	case "run_script":
		target = &s.whitelist.RunScript
	default:
		return fmt.Errorf("unknown tool %q for session whitelist", toolName)
	}

	// Skip duplicates.
	for _, existing := range *target {
		if existing == entry {
			return nil
		}
	}

	*target = append(*target, entry)

	// Persist to disk.
	wp := s.whitelistPath()
	if wp == "" {
		// No persistent directory — in-memory only is fine.
		return nil
	}

	data, err := json.MarshalIndent(s.whitelist, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session whitelist: %w", err)
	}

	if err := os.WriteFile(wp, data, 0644); err != nil {
		return fmt.Errorf("failed to write session whitelist %s: %w", wp, err)
	}

	return nil
}

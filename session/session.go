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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/logging"
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

// WithLogger sets the structured logger for session operations.
// If not set, all log output is silently discarded.
func WithLogger(l logging.Logger) SessionConfig {
	return func(s *Session) { s.log = l }
}

func (s *Session) logInfo(msg string, keyvals ...any) {
	if s.log != nil {
		s.log.Info(msg, keyvals...)
	}
}

func (s *Session) logError(msg string, err error, keyvals ...any) {
	if s.log != nil {
		s.log.Error(msg, err, keyvals...)
	}
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
		s.logError("createNewSession: ProjectDir not set", nil, "session_id", s.id, "agent", agentName)
		fmt.Fprintf(os.Stderr, "FATAL: session %s (agent=%s) created without ProjectDir\n", s.id, agentName)
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
// Sponsor is restored from stored session metadata (if previously persisted).
func Load(sessionID, agentName string, store SessionStore, opts ...SessionConfig) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("加载会话时必须提供会话存储")
	}

	info, err := store.GetMeta(context.Background(), sessionID)
	if err != nil {
		return nil, fmt.Errorf("会话 %q 未找到: %w", sessionID, err)
	}

	s := &Session{
		id:         sessionID,
		agentName:  agentName,
		sponsor:    info.Sponsor,
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
		s.logError("NewServiceSession: ProjectDir not set", nil, "session_id", id, "agent", agentName)
		fmt.Fprintf(os.Stderr, "FATAL: session %s (agent=%s) created without ProjectDir\n", id, agentName)
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

	// log is the structured logger for session operations.
	log logging.Logger

	// summarizer generates summaries during compaction
	summarizer Summarizer

	// mem stores context summaries for later retrieval
	mem MemoryStore

	// compactionHandler is called after each compaction event
	compactionHandler func(CompactionEvent)

	// compactStartHandler is called before TryCompact begins compaction.
	compactStartHandler func(windowTokens int64, maxWindowSize int64)

	// compactDoneHandler is called after TryCompact completes.
	compactDoneHandler func(messagesSlid int, windowTokens int64)

	// microCompactStartHandler is called before TryMicroCompact begins.
	microCompactStartHandler func(windowTokens int64, maxWindowSize int64)

	// microCompactDoneHandler is called after TryMicroCompact completes.
	microCompactDoneHandler func(compressed, deduped int, windowTokens int64)

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

// MaxWindowSize returns the configured maximum context window size in tokens.
// Returns 0 if automatic compaction is disabled.
func (s *Session) MaxWindowSize() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxWindowSize
}

// ── Store-level operations ──────────────────────────────────────────────
// These functions wrap SessionStore methods so that external code never
// calls SessionStore methods directly. All session operations go through
// the session package.

// ListSessions returns all sessions from the store.
func ListSessions(ctx context.Context, store SessionStore) ([]SessionInfo, error) {
	return store.ListSessions(ctx)
}

// CreateSession creates a new session in the store with the given agent name and options.
func CreateSession(ctx context.Context, store SessionStore, agentName string, opts ...SessionOption) (*SessionInfo, error) {
	return store.Create(ctx, agentName, opts...)
}

// DeleteSession removes a session from the store.
func DeleteSession(ctx context.Context, store SessionStore, sessionID string) error {
	return store.DeleteSession(ctx, sessionID)
}

// GetSessionMeta returns session metadata from the store.
func GetSessionMeta(ctx context.Context, store SessionStore, sessionID string) (*SessionInfo, error) {
	return store.GetMeta(ctx, sessionID)
}

// SetMaxWindowSize sets the maximum context window size in tokens.
// When the active window exceeds 80% of this value, compaction is triggered.
// A value of 0 or negative disables automatic compaction.
func (s *Session) SetMaxWindowSize(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxWindowSize = n
}

// CurrentWindowTokens estimates the token count of the active window
// (messages[cursor:]) using the same DeepSeek-based formula
// as MicroCompact/TryMicroCompact.
func (s *Session) CurrentWindowTokens() int64 {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()

	window := s.messages[s.cursor:]
	if len(window) == 0 {
		return 0
	}

	return estimateWindowTokensV2(window)
}

// ContextWindowUsage holds the current context window usage information.
// The calculation is consistent with the MicroCompact/TryMicroCompact method.
type ContextWindowUsage struct {
	// WindowTokens is the estimated token count of the active window.
	WindowTokens int64 `json:"window_tokens"`

	// MaxWindowSize is the configured maximum context window size in tokens.
	// If 0, compaction is disabled and usage ratio is undefined.
	MaxWindowSize int64 `json:"max_window_size"`

	// UsageRatio is the proportion of the active window relative to the max
	// window size (WindowTokens / MaxWindowSize). Ranges from 0.0 to 1.0+.
	// Returns 0 if MaxWindowSize is 0.
	UsageRatio float64 `json:"usage_ratio"`

	// MessageCount is the total number of messages in the session.
	MessageCount int `json:"message_count"`

	// Cursor is the current cursor position separating historical from active.
	Cursor int `json:"cursor"`

	// ActiveMessageCount is the number of messages in the active window.
	ActiveMessageCount int `json:"active_message_count"`
}

// ContextUsage returns the current context window usage information,
// using the same token estimation method as MicroCompact/TryMicroCompact.
func (s *Session) ContextUsage() ContextWindowUsage {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()

	window := s.messages[s.cursor:]
	windowTokens := estimateWindowTokensV2(window)
	mws := s.maxWindowSize

	var ratio float64
	if mws > 0 {
		ratio = float64(windowTokens) / float64(mws)
	}

	return ContextWindowUsage{
		WindowTokens:       windowTokens,
		MaxWindowSize:      mws,
		UsageRatio:         ratio,
		MessageCount:       len(s.messages),
		Cursor:             s.cursor,
		ActiveMessageCount: len(window),
	}
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
//
// Cursor 语义：cursor 是当前窗口的起始偏移量，messages 是完整历史。
// 当前窗口 = messages[cursor:]。TryCompact 清空 = cursor = len(messages)。
//
// This method implements lazy-loading: if messages haven't been loaded from the
// persistent store yet, it will automatically load them on first access.
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

// ensureLoaded implements lazy-loading for session messages.
// On first access (when loaded==false), it loads all messages from the persistent store.
//
// Cursor 语义：cursor 是当前窗口的起始偏移量（从 store.GetCursor 恢复）。
// messages 是完整历史，当前窗口 = messages[cursor:]。
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
		s.loaded = true
		return
	}

	// Restore cursor (偏移量) from persistent store
	cursor, cursorErr := s.store.GetCursor(ctx, s.id)
	if cursorErr != nil {
		cursor = 0
	}

	// Restore project directory from session metadata for file operations.
	var projectDir string
	if info, infoErr := s.store.GetMeta(ctx, s.id); infoErr == nil {
		projectDir = info.ProjectDir
	}

	s.mu.Lock()
	s.messages = msgs // 完整历史
	s.cursor = cursor // 偏移量：当前窗口 = messages[cursor:]
	if s.projectDir == "" {
		s.projectDir = projectDir
	}
	s.mu.Unlock()

	// Restore tracked modified files from store (if any).
	s.loadModifyFiles()

	s.loaded = true
}

// Restore explicitly loads historical messages from the persistent store into memory.
//
// NOTE: With the lazy-loading architecture (implemented in ensureLoaded),
// this method is OPTIONAL. Current() and Append() will automatically load
// messages on first access if they haven't been loaded yet.
//
// 偏移量模型：从 store 恢复 messages（完整历史）和 cursor（偏移量）。
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
		return fmt.Errorf("恢复会话 %s: %w", s.id, err)
	}

	// 与 ensureLoaded 一致：恢复 cursor 偏移量
	cursor, _ := s.store.GetCursor(ctx, s.id)

	s.mu.Lock()
	s.messages = msgs // 完整历史
	s.cursor = cursor // 偏移量
	s.mu.Unlock()

	s.loadingMu.Lock()
	s.loaded = true
	s.loadingMu.Unlock()

	return nil
}

// MarkAsContentRef finds a tool message by its ToolCallID within the active
// window and sets its Compacted field to refTag, then persists the change.
// Returns true if the message was found and updated.
func (s *Session) MarkAsContentRef(toolCallID, refTag string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := s.cursor; i < len(s.messages); i++ {
		if s.messages[i].Role == "tool" && s.messages[i].ToolCallID == toolCallID {
			s.messages[i].Compacted = refTag
			if s.store != nil {
				_ = s.store.UpdateMessages(context.Background(), s.id, s.cursor, s.messages)
			}
			return true
		}
	}
	return false
}

// Append adds new messages to the session.
//
// 偏移量模型：messages 是完整历史，cursor 是当前窗口起始偏移量。
// Append 只追加消息到 messages 末尾，cursor 保持不变（新消息自然落入
// 当前窗口 messages[cursor:]，因为它们在 cursor 之后）。
//
// 摘要触发（TryCompact）不在 Append 末尾调用 —— 改由 runtime 在新一个
// 轮次开始前调用，避免工具结果 append 中途触发清空破坏 tool_call 配对。
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
	// cursor 不变 —— 它是当前窗口的起始偏移量，Append 只追加到末尾

	if s.store != nil {
		for _, msg := range filtered {
			s.store.Append(ctx, s.id, s.agentName, s.sponsor, msg)
		}
	}

	s.mu.Unlock()
}

// TryCompact 检查当前会话窗口的 Token 是否超限，若超限则对活跃窗口
// (messages[cursor:]) 进行一次摘要、持久化到 MemoryStore，然后将 cursor
// 移到 messages 末尾（cursor = len(messages)），使当前窗口变为空切片。
//
// Cursor 语义（偏移量模型）：
//   - messages = 完整历史（不删除）
//   - cursor = 当前窗口起始偏移量
//   - 当前窗口 = messages[cursor:]
//   - 清空 = cursor = len(messages)（切片为空，但不删除历史消息）
//
// 触发条件：WindowTokens > 80% * maxWindowSize
// 调用时机：由 runtime 在新一个轮次开始前调用（不在 Append 末尾调用，
// 避免工具结果 append 中途触发清空破坏 tool_call 配对）
//
// 无锁设计：先 captureState（读锁快照）→ 无锁调用 summarizer（I/O）→
// 写锁内执行 cursor 移动。避免持锁期间进行 LLM 调用。
func (s *Session) TryCompact(ctx context.Context) {
	state := s.captureState()

	s.logInfo("TryCompact: entered", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"max_window_size", state.maxWindowSize,
		"active_messages", len(state.activeMessages))

	if !state.needsCompaction() {
		if state.maxWindowSize <= 0 {
			s.logInfo("TryCompact: skipped (maxWindowSize=0, compaction disabled)", "session_id", s.id)
		} else if len(state.activeMessages) == 0 {
			s.logInfo("TryCompact: skipped (active window is empty)", "session_id", s.id)
		} else {
			threshold := int64(float64(state.maxWindowSize) * 0.8)
			s.logInfo("TryCompact: skipped (windowTokens <= threshold)", "session_id", s.id,
				"window_tokens", state.windowTokens,
				"threshold", threshold)
		}
		return
	}

	s.logInfo("TryCompact: triggered (windowTokens > 80% of maxWindowSize)", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"max_window_size", state.maxWindowSize)

	// 触发 compact start handler
	if s.compactStartHandler != nil {
		s.compactStartHandler(state.windowTokens, s.maxWindowSize)
	}

	// 摘要当前活跃窗口（无锁，允许 LLM I/O 阻塞）
	slidCount := 0
	summaryFailed := false
	if s.summarizer != nil && len(state.activeMessages) > 0 {
		s.logInfo("TryCompact: generating summary", "session_id", s.id,
			"active_messages", len(state.activeMessages))
		chunks, err := s.generateSummary(ctx, state.activeMessages)
		if err != nil {
			s.logError("TryCompact: summary generation FAILED, will not compact", err, "session_id", s.id)
			summaryFailed = true
		} else {
			s.logInfo("TryCompact: summary generated", "session_id", s.id, "chunks", len(chunks))
			s.persistSummary(ctx, chunks)
			s.logInfo("TryCompact: summary persisted", "session_id", s.id)
		}
	} else if s.summarizer == nil {
		s.logInfo("TryCompact: no summarizer configured, skipping summary generation", "session_id", s.id)
	}

	if !summaryFailed {
		// 移动 cursor 到末尾（清空当前窗口，不删除历史消息）
		slidCount = s.executeFullCompaction(ctx)
		s.logInfo("TryCompact: cursor moved", "session_id", s.id, "slid_count", slidCount)
	} else {
		s.logInfo("TryCompact: skipped cursor movement due to summarization failure", "session_id", s.id)
	}

	// 触发 compact done handler
	afterTokens := s.CurrentWindowTokens()
	if s.compactDoneHandler != nil {
		s.compactDoneHandler(slidCount, afterTokens)
	}

	s.logInfo("TryCompact: done", "session_id", s.id, "after_tokens", afterTokens)
}

// ForceCompact 执行与 TryCompact 相同的压缩逻辑（摘要 + cursor 移动），
// 但使用独立阈值：仅当当前活跃窗口 tokens 超过 100K 时执行。
//
// 适用于前端手动触发的强制压缩（前端已通过 100K tokens 判断按钮可用性），
// 不应由 Runtime 自动调用。
func (s *Session) ForceCompact(ctx context.Context) {
	state := s.captureState()

	s.logInfo("ForceCompact: entered", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"active_messages", len(state.activeMessages))

	if len(state.activeMessages) == 0 {
		s.logInfo("ForceCompact: skipped (active window is empty)", "session_id", s.id)
		return
	}

	const forceCompactThreshold int64 = 100_000
	if state.windowTokens <= forceCompactThreshold {
		s.logInfo("ForceCompact: skipped (windowTokens <= threshold)", "session_id", s.id,
			"window_tokens", state.windowTokens,
			"threshold", forceCompactThreshold)
		return
	}

	s.logInfo("ForceCompact: triggered (windowTokens > threshold)", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"threshold", forceCompactThreshold)

	// 触发 compact start handler
	if s.compactStartHandler != nil {
		s.compactStartHandler(state.windowTokens, s.maxWindowSize)
	}

	// 摘要当前活跃窗口（无锁，允许 LLM I/O 阻塞）
	slidCount := 0
	summaryFailed := false
	if s.summarizer != nil && len(state.activeMessages) > 0 {
		s.logInfo("ForceCompact: generating summary", "session_id", s.id,
			"active_messages", len(state.activeMessages))
		chunks, err := s.generateSummary(ctx, state.activeMessages)
		if err != nil {
			s.logError("ForceCompact: summary generation FAILED, will not compact", err, "session_id", s.id)
			summaryFailed = true
		} else {
			s.logInfo("ForceCompact: summary generated", "session_id", s.id, "chunks", len(chunks))
			s.persistSummary(ctx, chunks)
			s.logInfo("ForceCompact: summary persisted", "session_id", s.id)
		}
	} else if s.summarizer == nil {
		s.logInfo("ForceCompact: no summarizer configured, skipping summary generation", "session_id", s.id)
	}

	if !summaryFailed {
		// 移动 cursor 到末尾（清空当前窗口，不删除历史消息）
		slidCount = s.executeFullCompaction(ctx)
		s.logInfo("ForceCompact: cursor moved", "session_id", s.id, "slid_count", slidCount)
	} else {
		s.logInfo("ForceCompact: skipped cursor movement due to summarization failure", "session_id", s.id)
	}

	// 触发 compact done handler
	afterTokens := s.CurrentWindowTokens()
	if s.compactDoneHandler != nil {
		s.compactDoneHandler(slidCount, afterTokens)
	}

	s.logInfo("ForceCompact: done", "session_id", s.id, "after_tokens", afterTokens)
}

// sessionState 是 compaction 决策用的会话状态快照。
//
// 偏移量模型：messages 是完整历史，cursor 是当前窗口起始偏移量，
// activeMessages = messages[cursor:] 是即将被清空的活跃窗口。
type sessionState struct {
	cursor         int
	messageCount   int
	maxWindowSize  int64
	windowTokens   int64
	messages       []Message
	activeMessages []Message // 当前活跃窗口 messages[cursor:]
}

// captureState 在读锁下原子地捕获当前会话状态。
// 偏移量模型：windowTokens 基于 activeMessages（messages[cursor:]）计算。
func (s *Session) captureState() sessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeMessages := s.messages[s.cursor:]
	tokens := estimateWindowTokensV2(activeMessages)

	return sessionState{
		cursor:         s.cursor,
		messageCount:   len(s.messages),
		maxWindowSize:  s.maxWindowSize,
		windowTokens:   tokens,
		messages:       s.messages,
		activeMessages: activeMessages,
	}
}

// needsCompaction 判断当前状态是否需要触发摘要清空。
// 偏移量模型：只要活跃窗口非空且 WindowTokens 超过 80% 阈值即触发。
func (st sessionState) needsCompaction() bool {
	if st.maxWindowSize <= 0 {
		return false
	}
	if len(st.activeMessages) == 0 {
		return false
	}

	threshold := int64(float64(st.maxWindowSize) * 0.8)
	return st.windowTokens > threshold
}

// executeFullCompaction 在写锁内执行清空：cursor = len(messages)。
//
// 偏移量模型核心动作：
//   - 不删除 messages（完整历史保留在内存和持久化存储中）
//   - 只把 cursor 移到 messages 末尾，使当前窗口 messages[cursor:] 变为空切片
//   - 通过 SetCursor 持久化新的 cursor 偏移量
//   - 不调用 UpdateMessages —— 保留 session.yml 中的完整原始消息
//
// 返回被清空的活跃窗口消息数（len(messages) - 旧 cursor）。
func (s *Session) executeFullCompaction(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 被清空的活跃窗口消息数
	slidCount := len(s.messages) - s.cursor

	// 移动 cursor 到末尾 —— 当前窗口 messages[cursor:] 变为空切片
	s.cursor = len(s.messages)

	if s.store != nil {
		// 只持久化 cursor 偏移量，不删除 session.yml 中的原始消息
		_ = s.store.SetCursor(ctx, s.id, s.cursor)
	}

	if s.compactionHandler != nil {
		s.compactionHandler(CompactionEvent{
			MessagesSlid:   slidCount,
			RemainingAfter: 0,
			WindowSize:     s.maxWindowSize,
		})
	}

	return slidCount
}

// generateSummary calls the summarizer outside of any locks to prevent deadlocks.
// sanitizeMessagesForLLM 全局清洗消息序列，确保符合 Anthropic API 的角色交替规则：
//   - 移除末尾未配对的 tool_call/tool_result
//   - 移除序列中任何 tool_call 但后续没有足够的 tool 消息跟随的 assistant 消息中的 tool_calls
//   - 移除没有对应 pending tool_call 的孤立 tool 消息
func sanitizeMessagesForLLM(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	// 第一遍：构建 tool_call 期望表
	type expectedCall struct {
		idx      int // 消息索引
		required int // 需要的 tool 消息数
		got      int // 实际收到的 tool 消息数
	}
	exp := make(map[string]*expectedCall) // callID → expected
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "tool" {
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				exp[tc.ID] = &expectedCall{idx: i, required: 1, got: 0}
			}
		}
	}
	// 统计每个 call_id 实际有多少 tool 消息
	for _, m := range msgs {
		if m.Role == "tool" {
			if e, ok := exp[m.ToolCallID]; ok {
				e.got++
			}
		}
	}

	// 找出不完整的 tool_calls（期待 > 实际）
	incomplete := make(map[string]bool)
	for id, e := range exp {
		if e.got < e.required {
			incomplete[id] = true
		}
	}

	// 第二遍：构建干净的输出
	out := make([]Message, 0, len(msgs))
	pendingIncomplete := false // 当前 assistant 是否有不完整的 tool_call
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// 检查这个 assistant 的 tool_calls 是否全部完整
			allComplete := true
			for _, tc := range m.ToolCalls {
				if incomplete[tc.ID] {
					allComplete = false
					break
				}
			}
			if allComplete {
				out = append(out, m)
				pendingIncomplete = false
			} else {
				// 这个 assistant 的 tool_calls 不完整 → 只保留文字内容，去掉 ToolCalls
				cleaned := m
				cleaned.ToolCalls = nil
				out = append(out, cleaned)
				pendingIncomplete = true
			}
		} else if m.Role == "tool" {
			if incomplete[m.ToolCallID] || pendingIncomplete {
				continue // 跳过孤立的 tool 消息
			}
			out = append(out, m)
		} else {
			out = append(out, m)
			pendingIncomplete = false
		}
	}

	return out
}

// 把所有待摘要消息完整交给 LLM，由 LLM 借助 prompt 的 Quality rules
// 自主判断哪些值得记忆（识别 trivial/重复/纠正/冲突）。代码层不做源头过滤，
// 因为砍掉消息会让 LLM 失去完整上下文，反而降低摘要质量。
// 但必须先剔除末尾未配对的 tool_call/tool_result（Anthropic API 校验）。
func (s *Session) generateSummary(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error) {
	if s.summarizer == nil || len(messages) == 0 {
		return nil, nil
	}

	// 剔除末尾未配对的 tool_call/tool_result，避免 LLM API 校验拒绝
	messages = sanitizeMessagesForLLM(messages)
	if len(messages) == 0 {
		s.logInfo("generateSummary: all messages stripped by LLM sanitization", "session_id", s.id)
		return nil, nil
	}

	chunks, err := s.summarizer.Summarize(ctx, messages)
	if err != nil {
		s.logError("generateSummary: Summarize failed", err, "session_id", s.id)
		return nil, err
	}
	if len(chunks) == 0 {
		s.logInfo("generateSummary: Summarize returned empty chunks (no substantive content)", "session_id", s.id)
		return nil, nil
	}

	// Enrich chunks with agent name, session ID, project dir, and timestamp fallback.
	// Timestamp 优先使用 LLM 在 JSON 中提供的事件时间；LLM 未填或解析失败时
	// 才 fallback 到 summarize 触发时间。
	for i := range chunks {
		chunks[i].AgentName = s.agentName
		chunks[i].SessionID = s.id
		chunks[i].ProjectDir = s.projectDir
		if chunks[i].Timestamp.IsZero() {
			chunks[i].Timestamp = time.Now()
		}
		if chunks[i].ID == "" && chunks[i].Content != "" {
			h := sha256.Sum256([]byte(chunks[i].Content))
			chunks[i].ID = hex.EncodeToString(h[:])
		}
	}

	return chunks, nil
}

// persistSummary stores the generated memory chunks in the memory store.
// Returns an error if storage fails; caller may still move cursor (best-effort storage).
func (s *Session) persistSummary(ctx context.Context, chunks []memory.MemoryChunk) error {
	if s.mem == nil || len(chunks) == 0 {
		return nil
	}

	if err := s.mem.StoreChunks(ctx, s.id, chunks); err != nil {
		s.logError("persistSummary: StoreChunks failed", err, "session_id", s.id)
		return err
	}
	return nil
}

// SetCompactionHandler updates the callback function invoked after compaction events.
// This can be called at any time to change or remove the handler.
//
// Pass nil to disable compaction notifications.
func (s *Session) SetCompactionHandler(h func(CompactionEvent)) {
	s.compactionHandler = h
}

// SetSummarizer sets the LLM-based summarizer for context compaction.
// This can be called at any time to change or remove the summarizer.
// Pass nil to disable summarization during compaction.
func (s *Session) SetSummarizer(ss Summarizer) {
	s.summarizer = ss
}

// SetMemory sets the memory store used for persisting compaction summaries.
// This can be called at any time to change or remove the memory store.
// Pass nil to disable summary persistence.
func (s *Session) SetMemory(mem MemoryStore) {
	s.mem = mem
}

// SetMicroCompactDoneHandler sets a callback invoked after TryMicroCompact completes.
// The callback receives (compressed, deduped, windowTokens) counters.
// Pass nil to disable.
func (s *Session) SetMicroCompactDoneHandler(h func(compressed, deduped int, windowTokens int64)) {
	s.microCompactDoneHandler = h
}

// SetCompactStartHandler sets a callback invoked before TryCompact begins
// LLM-based summarization compaction. The callback receives (windowTokens, maxWindowSize).
// Pass nil to disable.
func (s *Session) SetCompactStartHandler(h func(windowTokens, maxWindowSize int64)) {
	s.compactStartHandler = h
}

// SetCompactDoneHandler sets a callback invoked after TryCompact completes.
// The callback receives (messagesSlid, windowTokens).
// Pass nil to disable.
func (s *Session) SetCompactDoneHandler(h func(messagesSlid int, windowTokens int64)) {
	s.compactDoneHandler = h
}

// SetMicroCompactStartHandler sets a callback invoked before TryMicroCompact
// begins tool-message compression. The callback receives (windowTokens, maxWindowSize).
// Pass nil to disable.
func (s *Session) SetMicroCompactStartHandler(h func(windowTokens, maxWindowSize int64)) {
	s.microCompactStartHandler = h
}

// Compact manually triggers MicroCompact compression on the active window.
//
// 偏移量模型：只对活跃窗口 messages[cursor:] 执行无 LLM 压缩
// （tool 消息归档为占位符），保留 messages[:cursor] 历史分区不变。
// 压缩后的结果拼接回 messages，cursor 保持原位。
//
// Parameters:
//   - keepRecent: Number of recent assistant messages to retain in compacted form
func (s *Session) Compact(keepRecent int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 || s.cursor >= len(s.messages) {
		return
	}
	activeWindow := s.messages[s.cursor:]
	compacted := MicroCompact(activeWindow, keepRecent)
	// 重新拼接：保留历史分区 messages[:cursor] + 压缩后的活跃窗口
	newMessages := make([]Message, 0, s.cursor+len(compacted))
	newMessages = append(newMessages, s.messages[:s.cursor]...)
	newMessages = append(newMessages, compacted...)
	s.messages = newMessages
	// cursor 不变 —— 仍指向原历史分区的边界
	if s.store != nil {
		_ = s.store.UpdateMessages(context.Background(), s.id, s.cursor, s.messages)
	}
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
// 偏移量模型：截断消息后，若 cursor 超过截断点则回退到截断点，
// 避免 cursor 指向已不存在的消息。cursor 不会前进。
// Changes are persisted to the SessionStore if one is configured.
func (s *Session) Truncate(ctx context.Context, keepCount int) error {
	s.ensureLoaded(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()

	if keepCount < 0 {
		return fmt.Errorf("keepCount 必须 >= 0,但得到 %d", keepCount)
	}
	if keepCount >= len(s.messages) {
		return nil // nothing to truncate
	}

	s.messages = s.messages[:keepCount]
	// cursor 不能超过截断点，否则会指向不存在的消息
	if s.cursor > keepCount {
		s.cursor = keepCount
	}

	if s.store != nil {
		if err := s.store.Truncate(ctx, s.id, keepCount); err != nil {
			return fmt.Errorf("存储截断失败: %w", err)
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
		return fmt.Errorf("会话白名单中存在未知的工具 %q", toolName)
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
		return fmt.Errorf("序列化会话白名单失败: %w", err)
	}

	if err := os.WriteFile(wp, data, 0644); err != nil {
		return fmt.Errorf("写入会话白名单 %s 失败: %w", wp, err)
	}

	return nil
}

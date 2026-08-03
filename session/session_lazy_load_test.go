package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
)

// mockStore implements SessionStore for testing lazy-loading behavior.
type mockStore struct {
	mu          sync.RWMutex
	msgs        map[string][]Message
	appends     []appendRecord
	cursors     map[string]int // cursor persistence (internal to Session)
	modifyFiles map[string][]string

	// getMetaResult allows tests to configure what GetMeta returns.
	// If nil, GetMeta returns a default SessionInfo with empty ProjectDir.
	getMetaResult *SessionInfo
}

type appendRecord struct {
	sessionID string
	agentName string
	msg       Message
}

func newMockStore() *mockStore {
	return &mockStore{
		msgs:    make(map[string][]Message),
		cursors: make(map[string]int),
	}
}

func (m *mockStore) Append(_ context.Context, sessionID, agentName, sponsor string, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.msgs[sessionID] = append(m.msgs[sessionID], msg)
	m.appends = append(m.appends, appendRecord{
		sessionID: sessionID,
		agentName: agentName,
		msg:       msg,
	})
	return nil
}

func (m *mockStore) Get(_ context.Context, sessionID string) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs, ok := m.msgs[sessionID]
	if !ok {
		return nil, nil
	}

	result := make([]Message, len(msgs))
	copy(result, msgs)
	return result, nil
}

func (m *mockStore) CurrentContext(_ context.Context, _ string, _ int64) ([]Message, error) {
	return nil, nil
}

func (m *mockStore) Delete(_ context.Context, _ int64, _ string) error {
	return nil
}

func (m *mockStore) Clear(_ context.Context, _ string) error {
	return nil
}

func (m *mockStore) SetSlideHandler(_ SlideHandler) {}
func (m *mockStore) Close() error                   { return nil }
func (m *mockStore) DeleteSession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.msgs, sessionID)
	delete(m.cursors, sessionID)
	delete(m.modifyFiles, sessionID)
	return nil
}
func (m *mockStore) ListSessions(_ context.Context) ([]SessionInfo, error) {
	return nil, nil
}
func (m *mockStore) Create(_ context.Context, _ string, _ ...SessionOption) (*SessionInfo, error) {
	return &SessionInfo{SessionID: "test-sess"}, nil
}
func (m *mockStore) GetMeta(_ context.Context, _ string) (*SessionInfo, error) {
	if m.getMetaResult != nil {
		return m.getMetaResult, nil
	}
	return &SessionInfo{SessionID: "test-sess"}, nil
}
func (m *mockStore) ResolveSessionDir(_ string) (string, error) { return "", nil }
func (m *mockStore) GetCursor(_ context.Context, sessionID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if cursor, ok := m.cursors[sessionID]; ok {
		return cursor, nil
	}
	return 0, nil
}
func (m *mockStore) SetCursor(_ context.Context, sessionID string, cursor int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[sessionID] = cursor
	return nil
}

func (m *mockStore) SaveModifyFiles(sessionID string, files []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modifyFiles == nil {
		m.modifyFiles = make(map[string][]string)
	}
	if files == nil {
		delete(m.modifyFiles, sessionID)
	} else {
		m.modifyFiles[sessionID] = files
	}
	return nil
}

func (m *mockStore) GetModifyFiles(sessionID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.modifyFiles == nil {
		return nil, nil
	}
	return m.modifyFiles[sessionID], nil
}

func (m *mockStore) Truncate(_ context.Context, _ string, _ int) error {
	return nil // mock: no-op for lazy load tests
}

func (m *mockStore) UpdateMessages(_ context.Context, sessionID string, cursor int, messages []Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]Message, len(messages))
	copy(copied, messages)
	m.msgs[sessionID] = copied
	m.cursors[sessionID] = cursor
	return nil
}

// ── 懒加载核心测试 ───────────────────────────────────────────────────────

// TestLazyLoad_CurrentTriggersAutoLoad verifies that Current() automatically
// loads messages from store on first access.
func TestLazyLoad_CurrentTriggersAutoLoad(t *testing.T) {
	store := newMockStore()
	sessionID := "lazy-test-1"

	historicalMsgs := []Message{
		{Role: "user", Content: "搜索 Agent 新闻", Timestamp: time.Now().Unix()},
		{Role: "assistant", Content: "好的，让我搜索最新 Agent 相关新闻...", Timestamp: time.Now().Unix()},
		{Role: "user", Content: "继续", Timestamp: time.Now().Unix()},
	}
	store.msgs[sessionID] = historicalMsgs

	s := newTestSession(sessionID, "test-agent", store)

	if s.loaded {
		t.Error("New session should not be marked as loaded")
	}

	current := s.Current()

	if len(current) != len(historicalMsgs) {
		t.Errorf("Current() returned %d messages after lazy-load, want %d", len(current), len(historicalMsgs))
	}

	for i, msg := range current {
		if msg.Role != historicalMsgs[i].Role || msg.Content != historicalMsgs[i].Content {
			t.Errorf("Current()[%d] = %+v, want %+v", i, msg, historicalMsgs[i])
		}
	}

	if !s.loaded {
		t.Error("Session should be marked as loaded after Current()")
	}
}

// TestLazyLoad_CrossRequestSessionResume simulates the real-world "继续" scenario:
// Request 1: User asks question → Session created, messages appended, stored to disk
// Request 2: User says "继续" → NEW Session created → should auto-load history from disk
func TestLazyLoad_CrossRequestSessionResume(t *testing.T) {
	store := newMockStore()
	sessionID := "resume-test"
	agentName := "coder"

	// ========== REQUEST 1: Initial conversation ==========
	session1 := newTestSession(sessionID, agentName, store)

	session1.Append(context.Background(), Message{
		Role: "user", Content: "搜索 Agent 新闻", Timestamp: time.Now().Unix(),
	})

	session1.Append(context.Background(), Message{
		Role: "assistant", Content: "好的，这是搜索结果...", Timestamp: time.Now().Unix(),
	})

	if len(session1.All()) != 2 {
		t.Fatalf("Request 1: Expected 2 messages, got %d", len(session1.All()))
	}

	if len(store.msgs[sessionID]) != 2 {
		t.Fatalf("Request 1: Store should have 2 messages, got %d", len(store.msgs[sessionID]))
	}

	// ========== REQUEST 2: User says "继续" ==========
	session2 := newTestSession(sessionID, agentName, store)

	if session2.loaded {
		t.Error("session2 should NOT be loaded yet")
	}

	session2.Append(context.Background(), Message{
		Role: "user", Content: "继续", Timestamp: time.Now().Unix(),
	})

	allMsgs := session2.All()
	expectedTotal := 3
	if len(allMsgs) != expectedTotal {
		t.Errorf("CRITICAL FAILURE: After '继续', session has %d messages, want %d\n"+
			"This means the LLM will NOT see previous context!\n"+
			"Messages: %+v", len(allMsgs), expectedTotal, allMsgs)
	}

	if allMsgs[0].Content != "搜索 Agent 新闻" {
		t.Error("First message should be original question")
	}
	if allMsgs[1].Content != "好的，这是搜索结果..." {
		t.Error("Second message should be original answer")
	}
	if allMsgs[2].Content != "继续" {
		t.Error("Third message should be '继续'")
	}

	current := session2.Current()
	if len(current) != 3 {
		t.Errorf("Current() returned %d messages, want 3", len(current))
	}
}

// TestLazyLoad_ConcurrentAccess tests thread safety of the lazy-loading mechanism.
func TestLazyLoad_ConcurrentAccess(t *testing.T) {
	store := newMockStore()
	sessionID := "concurrent-test"

	store.msgs[sessionID] = []Message{
		{Role: "user", Content: "initial", Timestamp: time.Now().Unix()},
	}

	s := newTestSession(sessionID, "agent", store)

	var wg sync.WaitGroup
	const numGoroutines = 50
	results := make(chan []Message, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msgs := s.Current()
			results <- msgs
		}(i)
	}

	wg.Wait()
	close(results)

	count := 0
	for msgs := range results {
		count++
		if len(msgs) != 1 {
			t.Errorf("Goroutine result[%d] has %d messages, want 1", count, len(msgs))
		}
	}

	if count != numGoroutines {
		t.Errorf("Only received %d results, want %d", count, numGoroutines)
	}

	if !s.loaded {
		t.Error("Session should be marked as loaded after concurrent access")
	}
}

// TestLazyLoad_NewSessionWithNoHistory tests edge case:
// Brand new session with no prior messages in store.
func TestLazyLoad_NewSessionWithNoHistory(t *testing.T) {
	store := newMockStore()
	sessionID := "brand-new-session"

	s := newTestSession(sessionID, "agent", store)

	current := s.Current()
	if current != nil {
		t.Errorf("Brand new session: Current() should return nil, got %d messages", len(current))
	}

	if !s.loaded {
		t.Error("Brand new session should still be marked as loaded after Current()")
	}

	s.Append(context.Background(), Message{
		Role: "user", Content: "first message", Timestamp: time.Now().Unix(),
	})

	all := s.All()
	if len(all) != 1 {
		t.Errorf("After append, All() returned %d messages, want 1", len(all))
	}
}

// TestLazyLoad_WithoutStore tests pure in-memory mode.
func TestLazyLoad_WithoutStore(t *testing.T) {
	s := newTestSession("memory-only", "agent", nil)

	s.Append(context.Background(), Message{
		Role: "user", Content: "hello", Timestamp: time.Now().Unix(),
	})

	current := s.Current()
	if len(current) != 1 {
		t.Errorf("Memory-only session: Current() returned %d messages, want 1", len(current))
	}

	if !s.loaded {
		t.Error("Memory-only session should be marked as loaded")
	}
}

// TestLazyLoad_ProjectDirLoadedFromStore verifies that ensureLoaded loads
// ProjectDir from the store's GetMeta when projectDir is empty.
func TestLazyLoad_ProjectDirLoadedFromStore(t *testing.T) {
	expectedProjectDir := "/home/user/my-project"
	store := newMockStore()
	store.getMetaResult = &SessionInfo{
		SessionID:  "project-dir-test",
		ProjectDir: expectedProjectDir,
	}

	// 直接构造，projectDir 留空，模拟 Load() 场景
	s := initSession("project-dir-test", "agent", "", "", store, logging.NewNopLogger())

	if s.ProjectDir() != "" {
		t.Errorf("Before lazy-load: ProjectDir() = %q, want empty", s.ProjectDir())
	}

	_ = s.Current()

	if s.ProjectDir() != expectedProjectDir {
		t.Errorf("After lazy-load: ProjectDir() = %q, want %q", s.ProjectDir(), expectedProjectDir)
	}

	if !s.loaded {
		t.Error("Session should be marked as loaded after Current()")
	}
}

// TestCursorBehavior_CurrentVsAll_WithCompaction verifies the CRITICAL distinction:
// - All(): Returns ALL messages (complete history)
// - Current(): Returns only the ACTIVE WINDOW (messages[cursor:])
// 注意：Append 不会自动触发 TryCompact，需要手动调用
func TestCursorBehavior_CurrentVsAll_WithCompaction(t *testing.T) {
	store := newMockStore()
	sessionID := "cursor-with-compact"

	s := newTestSession(sessionID, "agent", store,
		WithModelContextResolver(func() int64 { return 100 }),
		WithMemory(newInMemoryMemory()), // 必须配置 mem，否则 persistCompactionChunks 会失败
		WithCompactor(&mockCompactor{
			CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
				return []memory.MemoryChunk{{Summary: "summary", Content: "compacted"}}, nil
			},
		}),
	)

	for i := 0; i < 20; i++ {
		s.Append(context.Background(), Message{
			Role: "user", Content: fmt.Sprintf("message number %d with enough text to exceed token limit", i),
			Timestamp: time.Now().Unix(),
		})
	}

	// 手动触发压缩
	s.TryCompact(context.Background())

	all := s.All()
	current := s.Current()

	t.Logf("After appending 20 messages and TryCompact:")
	t.Logf("  All() returned:   %d messages", len(all))
	t.Logf("  Current() returned: %d messages", len(current))
	t.Logf("  Session cursor:   %d", s.cursor)

	// All() 应返回完整历史（20 条）
	if len(all) != 20 {
		t.Errorf("All() should return all 20 messages, got %d", len(all))
	}

	// Current() 应返回空（cursor 已移到末尾）
	if len(current) != 0 {
		t.Errorf("Current() should return 0 messages after compaction, got %d", len(current))
	}

	if s.cursor != 20 {
		t.Errorf("cursor should be 20 after compaction, got %d", s.cursor)
	}
}

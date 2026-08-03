package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
)

// newTestSession 创建具有指定 ID 的测试会话。
// 用于需要控制 session ID 的测试场景（如 mock store 交互）。
func newTestSession(id, agentName string, store SessionStore, opts ...SessionConfig) *Session {
	return initSession(id, agentName, "", "/tmp/test", store, logging.NewNopLogger(), opts...)
}

// ── 构造函数测试 ─────────────────────────────────────────────────────────

func TestNew_BasicCreation(t *testing.T) {
	s, err := New("test-agent", "", "/tmp/test", newMockStore(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if s.ID() == "" {
		t.Error("New() should generate a non-empty ID")
	}
	if s.AgentName() != "test-agent" {
		t.Errorf("New() AgentName = %v, want %v", s.AgentName(), "test-agent")
	}
	if s.ProjectDir() != "/tmp/test" {
		t.Errorf("New() ProjectDir = %v, want %v", s.ProjectDir(), "/tmp/test")
	}
}

// ── 消息操作测试 ─────────────────────────────────────────────────────────

func TestSession_AppendAndRetrieve(t *testing.T) {
	s := newTestSession("session-append", "agent", newMockStore())

	msg1 := Message{Role: "user", Content: "Hello", Timestamp: time.Now().Unix()}
	msg2 := Message{Role: "assistant", Content: "Hi there!", Timestamp: time.Now().Unix()}

	s.Append(context.Background(), msg1, msg2)

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("Append() resulted in %d messages, want 2", len(all))
	}

	current := s.Current()
	if len(current) != 2 {
		t.Errorf("Current() returned %d messages after append, want 2", len(current))
	}
}

func TestSession_Reset(t *testing.T) {
	s := newTestSession("session-reset", "agent", newMockStore())

	s.Append(context.Background(), Message{
		Role:      "user",
		Content:   "important data",
		Timestamp: time.Now().Unix(),
	})

	if len(s.All()) == 0 {
		t.Error("Append should have added messages before reset")
	}

	s.Reset()

	if len(s.All()) != 0 {
		t.Error("Reset() should clear all messages")
	}

	current := s.Current()
	if current != nil {
		t.Error("Reset() should make Current() return nil")
	}
}

func TestSession_ConcurrentAccess(t *testing.T) {
	s := newTestSession("session-concurrent", "agent", newMockStore())
	var wg sync.WaitGroup

	numGoroutines := 10
	messagesPerGoroutine := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				s.Append(context.Background(), Message{
					Role:      "user",
					Content:   "concurrent message",
					Timestamp: time.Now().Unix(),
				})
				_ = s.Current()
				_ = s.All()
			}
		}(i)
	}

	wg.Wait()

	finalCount := len(s.All())
	expectedCount := numGoroutines * messagesPerGoroutine

	if finalCount != expectedCount {
		t.Errorf("Concurrent access: expected %d messages, got %d", expectedCount, finalCount)
	}
}

// ── 压缩相关测试 ─────────────────────────────────────────────────────────

func TestSession_TryCompact(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return []memory.MemoryChunk{
				{Summary: "summary of old messages", Content: "compacted content"},
			}, nil
		},
	}

	compactionEvents := []CompactionEvent{}
	handler := func(event CompactionEvent) {
		compactionEvents = append(compactionEvents, event)
	}

	s := newTestSession("session-window-size", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 100 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()), // 必须配置 mem，否则 persistCompactionChunks 会失败
		WithCompactionHandler(handler),
	)

	longContent := string(make([]byte, 400))
	for i := 0; i < 5; i++ {
		s.Append(context.Background(), Message{
			Role:      "user",
			Content:   longContent,
			Timestamp: time.Now().Unix(),
		})
	}

	// 触发压缩
	s.TryCompact(context.Background())

	if !compactorCalled {
		t.Error("Compactor should have been called when window exceeded threshold")
	}

	if len(compactionEvents) == 0 {
		t.Error("Compaction handler should have been called at least once")
	}
}

// TestSession_TryCompact_MemNil_SkipsCompactor 验证未配置记忆存储（mem==nil）时
// doCompact 在入口即跳过，不调用 compactor —— 避免白烧 LLM token 且 cursor 永不移动的死循环。
func TestSession_TryCompact_MemNil_SkipsCompactor(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return nil, nil
		},
	}

	// 故意不 WithMemory —— mem 保持 nil
	s := newTestSession("session-mem-nil", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 100 }),
		WithCompactor(mockComp),
	)

	longContent := string(make([]byte, 400))
	for i := 0; i < 5; i++ {
		s.Append(context.Background(), Message{
			Role:      "user",
			Content:   longContent,
			Timestamp: time.Now().Unix(),
		})
	}

	s.TryCompact(context.Background())

	if compactorCalled {
		t.Error("mem 为 nil 时不应调用 compactor（会白烧 LLM token）")
	}
	// cursor 不应移动，活跃窗口仍包含全部消息
	if got := len(s.Current()); got != 5 {
		t.Errorf("mem 为 nil 时 cursor 不应移动，Current() = %d 条，期望 5", got)
	}
}

// TestSession_TryCompact_CooldownAfterFailure 验证压缩失败后 TryCompact 进入冷却，
// 冷却期内再次触发不会调用 compactor —— 避免 LLM 失败/空返回时每轮重试的死循环。
func TestSession_TryCompact_CooldownAfterFailure(t *testing.T) {
	callCount := 0
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			callCount++
			return nil, errors.New("模拟 LLM 失败")
		},
	}

	s := newTestSession("session-cooldown", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 100 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()),
	)

	longContent := string(make([]byte, 400))
	for i := 0; i < 5; i++ {
		s.Append(context.Background(), Message{
			Role:      "user",
			Content:   longContent,
			Timestamp: time.Now().Unix(),
		})
	}

	// 第一次：触发压缩，compactor 被调，失败，记录冷却时间戳
	s.TryCompact(context.Background())
	if callCount != 1 {
		t.Fatalf("第一次 TryCompact 应调用 compactor 1 次，实际 %d", callCount)
	}

	// 第二次：冷却期内（5min），应直接跳过，compactor 不再被调
	s.TryCompact(context.Background())
	if callCount != 1 {
		t.Errorf("冷却期内第二次 TryCompact 不应调用 compactor，实际调用 %d 次", callCount)
	}

	// 手动把冷却时间戳设到 5min 前，模拟冷却过期，第三次应再次调用
	s.lastCompactionFailAt.Store(time.Now().Add(-compactionCooldown - time.Second).UnixNano())
	s.TryCompact(context.Background())
	if callCount != 2 {
		t.Errorf("冷却过期后第三次 TryCompact 应再次调用 compactor，实际调用 %d 次", callCount)
	}
}

// mockCompactor 实现 Compactor 接口用于测试。
// generateCompactionChunks 通过 s.compactor.Compact 调用。
type mockCompactor struct {
	CompactFunc func(ctx context.Context, s *Session, messages []Message) ([]memory.MemoryChunk, error)
}

func (m *mockCompactor) Compact(ctx context.Context, s *Session, messages []Message) ([]memory.MemoryChunk, error) {
	if m.CompactFunc != nil {
		return m.CompactFunc(ctx, s, messages)
	}
	return nil, nil
}

// ── 内存存储测试 ─────────────────────────────────────────────────────────

func TestInMemoryMemory_StoreAndRetrieve(t *testing.T) {
	store := newInMemoryMemory()
	ctx := context.Background()

	chunks := []memory.MemoryChunk{
		{Summary: "title-1", Content: "content-1", Tags: []string{}},
		{Summary: "title-2", Content: "content-2", Tags: []string{}},
	}
	err := store.StoreChunks(ctx, "session-1", chunks)
	if err != nil {
		t.Fatalf("StoreChunks() unexpected error: %v", err)
	}

	results, err := store.Retrieve(ctx, "", "session-1", 10)
	if err != nil {
		t.Fatalf("Retrieve() unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Retrieve() returned %d items, want 2", len(results))
	}

	if results[0].Content != "content-1" || results[1].Content != "content-2" {
		t.Errorf("Retrieve() content mismatch: %v", results)
	}
}

func TestInMemoryMemory_RetrieveLimit(t *testing.T) {
	store := newInMemoryMemory()
	ctx := context.Background()

	chunks := make([]memory.MemoryChunk, 10)
	for i := 0; i < 10; i++ {
		chunks[i] = memory.MemoryChunk{
			Summary: "title",
			Content: "content",
			Tags:    []string{},
		}
	}
	store.StoreChunks(ctx, "session-limit", chunks)

	results, err := store.Retrieve(ctx, "", "session-limit", 5)
	if err != nil {
		t.Fatalf("Retrieve() unexpected error: %v", err)
	}

	if len(results) != 5 {
		t.Errorf("Retrieve() with limit=5 returned %d items, want 5", len(results))
	}
}

func TestInMemoryMemory_NonExistentSession(t *testing.T) {
	store := newInMemoryMemory()
	ctx := context.Background()

	results, err := store.Retrieve(ctx, "", "non-existent", 10)
	if err != nil {
		t.Fatalf("Retrieve() for non-existent session should not error, got: %v", err)
	}

	if results != nil {
		t.Errorf("Retrieve() for non-existent session should return nil, got %v", results)
	}
}

// ── 配置选项测试 ─────────────────────────────────────────────────────────

func TestSessionConfig_FunctionalOptions(t *testing.T) {
	s := newTestSession("session-options", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 5000 }),
	)

	if s.ModelContextLength() != 5000 {
		t.Errorf("WithModelContextResolver not applied, got %v", s.ModelContextLength())
	}

	// 未注入 resolver 时返回 0（禁用压缩）
	s2 := newTestSession("session-options-nil", "agent", newMockStore())
	if s2.ModelContextLength() != 0 {
		t.Errorf("nil resolver should return 0, got %v", s2.ModelContextLength())
	}

	mockMem := &mockMemoryStoreImpl{}
	s3 := newTestSession("session-memory", "agent", newMockStore(),
		WithMemory(mockMem),
	)

	if s3.mem == nil {
		t.Error("WithMemory not applied correctly")
	}
}

type mockMemoryStoreImpl struct {
	stored []memory.MemoryChunk
}

func (m *mockMemoryStoreImpl) StoreChunks(_ context.Context, _ string, chunks []memory.MemoryChunk) error {
	m.stored = append(m.stored, chunks...)
	return nil
}

func (m *mockMemoryStoreImpl) Retrieve(_ context.Context, _ string, _ string, _ int) ([]memory.MemoryChunk, error) {
	out := make([]memory.MemoryChunk, len(m.stored))
	copy(out, m.stored)
	return out, nil
}

// ── 性能测试 ─────────────────────────────────────────────────────────────

func BenchmarkSession_Append(b *testing.B) {
	s := newTestSession("bench-session", "agent", newMockStore())
	msg := Message{Role: "user", Content: "benchmark message", Timestamp: time.Now().Unix()}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Append(context.Background(), msg)
	}
}

func BenchmarkSession_ConcurrentAppend(b *testing.B) {
	s := newTestSession("bench-concurrent", "agent", newMockStore())
	msg := Message{Role: "user", Content: "benchmark message", Timestamp: time.Now().Unix()}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Append(context.Background(), msg)
		}
	})
}

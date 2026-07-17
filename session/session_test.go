package session

import (
	"context"
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
	summarizerCalled := false
	mockSummarizer := &mockSummarizer{
		SummarizeFunc: func(ctx context.Context, msgs []Message) ([]memory.MemoryChunk, error) {
			summarizerCalled = true
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
		WithMaxWindowSize(100),
		WithSummarizer(mockSummarizer),
		WithMemory(newInMemoryMemory()), // 必须配置 mem，否则 persistSummary 会失败
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

	// Trigger compaction
	s.TryCompact(context.Background())

	if !summarizerCalled {
		t.Error("Summarizer should have been called when window exceeded threshold")
	}

	if len(compactionEvents) == 0 {
		t.Error("Compaction handler should have been called at least once")
	}
}

type mockSummarizer struct {
	SummarizeFunc func(ctx context.Context, msgs []Message) ([]memory.MemoryChunk, error)
}

func (m *mockSummarizer) Summarize(ctx context.Context, msgs []Message) ([]memory.MemoryChunk, error) {
	if m.SummarizeFunc != nil {
		return m.SummarizeFunc(ctx, msgs)
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
		WithMaxWindowSize(5000),
	)

	if s.maxWindowSize != 5000 {
		t.Errorf("WithMaxWindowSize not applied, got %v", s.maxWindowSize)
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

package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewSession_BasicCreation(t *testing.T) {
	s := NewSession("test-session-1", "test-agent")

	if s.ID() != "test-session-1" {
		t.Errorf("NewSession() ID = %v, want %v", s.ID(), "test-session-1")
	}
	if s.AgentName() != "test-agent" {
		t.Errorf("NewSession() AgentName = %v, want %v", s.AgentName(), "test-agent")
	}
	if s.ProjectDir() != "" {
		t.Errorf("NewSession() ProjectDir should be empty by default, got %v", s.ProjectDir())
	}
}

func TestSession_AppendAndRetrieve(t *testing.T) {
	s := NewSession("session-append", "agent")

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

func TestSession_CurrentWindow(t *testing.T) {
	s := NewSession("session-window", "agent")

	for i := 0; i < 5; i++ {
		s.Append(context.Background(), Message{
			Role:      "user",
			Content:   "message",
			Timestamp: time.Now().Unix(),
		})
		s.Append(context.Background(), Message{
			Role:      "assistant",
			Content:   "response",
			Timestamp: time.Now().Unix(),
		})
	}

	current := s.Current()
	expectedCount := 10
	if len(current) != expectedCount {
		t.Errorf("Current() = %d messages, want %d", len(current), expectedCount)
	}
}

func TestSession_Reset(t *testing.T) {
	s := NewSession("session-reset", "agent")

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

func TestSession_Compact(t *testing.T) {
	s := NewSession("session-compact", "agent")

	for i := 0; i < 10; i++ {
		s.Append(context.Background(), Message{
			Role:      "user",
			Content:   "question",
			Timestamp: time.Now().Unix(),
		})
		s.Append(context.Background(), Message{
			Role:      "assistant",
			Content:   "answer",
			Timestamp: time.Now().Unix(),
		})
	}

	initialCount := len(s.All())
	if initialCount != 20 {
		t.Fatalf("Expected 20 messages before compact, got %d", initialCount)
	}

	s.Compact(2)

	afterCompact := s.All()

	if len(afterCompact) == 0 && initialCount > 0 {
		t.Error("Compact() should not remove all messages")
	}
}

func TestSession_ConcurrentAccess(t *testing.T) {
	s := NewSession("session-concurrent", "agent")
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

func TestSession_WithMaxWindowSize(t *testing.T) {
	summarizerCalled := false
	mockSummarizer := &mockSummarizer{
		SummarizeFunc: func(ctx context.Context, msgs []Message) (string, error) {
			summarizerCalled = true
			return "summary of old messages", nil
		},
	}

	compactionEvents := []CompactionEvent{}
	handler := func(event CompactionEvent) {
		compactionEvents = append(compactionEvents, event)
	}

	s := NewSession("session-window-size", "agent",
		WithMaxWindowSize(100),
		WithSummarizer(mockSummarizer),
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

	if !summarizerCalled {
		t.Error("Summarizer should have been called when window exceeded threshold")
	}

	if len(compactionEvents) == 0 {
		t.Error("Compaction handler should have been called at least once")
	}
}

func TestSession_SetCompactionHandler(t *testing.T) {
	s := NewSession("session-handler", "agent")

	eventsReceived := []CompactionEvent{}
	handler := func(event CompactionEvent) {
		eventsReceived = append(eventsReceived, event)
	}

	s.SetCompactionHandler(handler)

	if len(eventsReceived) != 0 {
		t.Error("Handler should not be called just from setting it")
	}

	newHandler := func(event CompactionEvent) {}
	s.SetCompactionHandler(newHandler)

	s.SetCompactionHandler(func(event CompactionEvent) {})
}

type mockSummarizer struct {
	SummarizeFunc func(ctx context.Context, msgs []Message) (string, error)
}

func (m *mockSummarizer) Summarize(ctx context.Context, msgs []Message) (string, error) {
	if m.SummarizeFunc != nil {
		return m.SummarizeFunc(ctx, msgs)
	}
	return "", nil
}

func TestInMemoryMemory_StoreAndRetrieve(t *testing.T) {
	store := newInMemoryMemory()
	ctx := context.Background()

	err := store.Store(ctx, "session-1", "title-1", "content-1")
	if err != nil {
		t.Fatalf("Store() unexpected error: %v", err)
	}

	err = store.Store(ctx, "session-1", "title-2", "content-2")
	if err != nil {
		t.Fatalf("Store() second call unexpected error: %v", err)
	}

	results, err := store.Retrieve(ctx, "", "session-1", 10)
	if err != nil {
		t.Fatalf("Retrieve() unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Retrieve() returned %d items, want 2", len(results))
	}

	if results[0] != "content-1" || results[1] != "content-2" {
		t.Errorf("Retrieve() content mismatch: %v", results)
	}
}

func TestInMemoryMemory_RetrieveLimit(t *testing.T) {
	store := newInMemoryMemory()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		store.Store(ctx, "session-limit", "title", "content")
	}

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

func TestMicroCompact_PreservesRecentAssistantMessages(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "tool", Content: "[Read] result"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "tool", Content: "[Grep] grep result"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}

	compacted := MicroCompact(messages, 1)

	hasMostRecentAssistant := false
	for _, msg := range compacted {
		if msg.Content == "a3" && msg.Role == "assistant" {
			hasMostRecentAssistant = true
		}
	}

	if !hasMostRecentAssistant {
		t.Error("MicroCompact should keep the most recent assistant message (a3)")
	}
}

// TestMicroCompact_ReadOnlyByAssistantToolCallName covers the fix for the case
// where a tool message's Content does NOT start with "[ToolName]" (which happens
// whenever the tool returns a raw string). The read-only detection should fall
// back to the tool name resolved from the preceding assistant.ToolCalls list.
func TestMicroCompact_ReadOnlyByAssistantToolCallName(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "q1"},
		{
			Role: "assistant", Content: "a1",
			ToolCalls: []ToolCall{
				{ID: "call_r1", Name: "Read", Arguments: "{}"},
			},
		},
		// Tool message whose Content does NOT have the [Read] prefix. Previously
		// the read-only detection would miss this and never drop it.
		{Role: "tool", Content: "raw file content", ToolCallID: "call_r1"},
		{Role: "user", Content: "q2"},
		{
			Role: "assistant", Content: "a2",
			ToolCalls: []ToolCall{
				{ID: "call_p1", Name: "PowerShell", Arguments: "{}"},
			},
		},
		{Role: "tool", Content: "powershell result", ToolCallID: "call_p1"},
	}

	compacted := MicroCompact(messages, 1)

	// We keep 1 recent assistant -> keepFromIdx is the index of "a2" -> the
	// tool message for "call_r1" (Read) is in the dropped section and should
	// be dropped. The tool message for "call_p1" (PowerShell) is in the
	// kept section and should be kept.
	for _, msg := range compacted {
		if msg.Role == "tool" && msg.ToolCallID == "call_r1" {
			t.Errorf("Read tool result should be dropped, but it was kept: %+v", msg)
		}
	}

	// The assistant message for "a1" (which is in the dropped section, since
	// keepRecent=1) should have its read-only tool_call removed to keep
	// the message stream consistent.
	for _, msg := range compacted {
		if msg.Role == "assistant" && msg.Content == "a1" {
			if len(msg.ToolCalls) != 0 {
				t.Errorf("a1's ToolCalls should be empty after compacting, got %+v", msg.ToolCalls)
			}
		}
	}
}

// TestMicroCompact_PreservesReasoningContent ensures the post-processing pass
// that strips dropped tool_calls does NOT lose the assistant's ReasoningContent
// (used by DeepSeek-R1 / QwQ style thinking models).
func TestMicroCompact_PreservesReasoningContent(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "q1"},
		{
			Role:             "assistant",
			Content:          "a1",
			ReasoningContent: "thinking hard about q1",
			ToolCalls: []ToolCall{
				{ID: "call_r1", Name: "Read", Arguments: "{}"},
			},
		},
		{Role: "tool", Content: "[Read] ok", ToolCallID: "call_r1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2", ReasoningContent: "thinking about q2"},
	}

	compacted := MicroCompact(messages, 1)

	for _, msg := range compacted {
		if msg.Role == "assistant" && msg.Content == "a1" {
			if msg.ReasoningContent != "thinking hard about q1" {
				t.Errorf("ReasoningContent was lost during MicroCompact: %+v", msg)
			}
		}
	}
}

func TestIsReadOnlyTool(t *testing.T) {
	readOnlyTools := []string{"Read", "Grep", "Glob", "WebSearch", "WebFetch", "Skill", "AskUser"}
	writeTools := []string{"Write", "Bash", "Edit"}

	for _, tool := range readOnlyTools {
		if !IsReadOnlyTool(tool) {
			t.Errorf("IsReadOnlyTool(%s) should return true", tool)
		}
	}

	for _, tool := range writeTools {
		if IsReadOnlyTool(tool) {
			t.Errorf("IsReadOnlyTool(%s) should return false", tool)
		}
	}
}

func TestCompactionEvent_Fields(t *testing.T) {
	event := CompactionEvent{
		MessagesSlid:   5,
		RemainingAfter: 15,
		WindowSize:     8000,
	}

	if event.MessagesSlid != 5 {
		t.Errorf("CompactionEvent.MessagesSlid = %v, want 5", event.MessagesSlid)
	}
	if event.RemainingAfter != 15 {
		t.Errorf("CompactionEvent.RemainingAfter = %v, want 15", event.RemainingAfter)
	}
	if event.WindowSize != 8000 {
		t.Errorf("CompactionEvent.WindowSize = %v, want 8000", event.WindowSize)
	}
}

func TestSessionConfig_FunctionalOptions(t *testing.T) {

	s := NewSession("session-options", "agent",
		WithMaxWindowSize(5000),
	)

	if s.maxWindowSize != 5000 {
		t.Errorf("WithMaxWindowSize not applied, got %v", s.maxWindowSize)
	}

	mockMem := &mockMemoryStoreImpl{}
	s3 := NewSession("session-memory", "agent",
		WithMemory(mockMem),
	)

	if s3.mem == nil {
		t.Error("WithMemory not applied correctly")
	}
}

type mockMemoryStoreImpl struct {
	stored []string
}

func (m *mockMemoryStoreImpl) Store(_ context.Context, _ string, _ string, content string) error {
	m.stored = append(m.stored, content)
	return nil
}

func (m *mockMemoryStoreImpl) Retrieve(_ context.Context, _ string, _ string, _ int) ([]string, error) {
	return m.stored, nil
}

func TestMessage_Structure(t *testing.T) {
	ts := int64(1672531200)

	msg := Message{
		Role:      "assistant",
		Content:   "Hello!",
		Timestamp: ts,
		ToolCalls: []ToolCall{
			{ID: "call_1", Name: "Read", Arguments: `{"path": "/file.txt"}`},
		},
	}

	if msg.Role != "assistant" {
		t.Errorf("Message.Role = %v, want assistant", msg.Role)
	}
	if msg.Content != "Hello!" {
		t.Errorf("Message.Content = %v, want Hello!", msg.Content)
	}
	if msg.Timestamp != ts {
		t.Errorf("Message.Timestamp = %v, want %v", msg.Timestamp, ts)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("Message.ToolCalls length = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Name != "Read" {
		t.Errorf("ToolCall.Name = %v, want Read", msg.ToolCalls[0].Name)
	}
}

func BenchmarkSession_Append(b *testing.B) {
	s := NewSession("bench-session", "agent")
	msg := Message{Role: "user", Content: "benchmark message", Timestamp: time.Now().Unix()}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Append(context.Background(), msg)
	}
}

func BenchmarkSession_ConcurrentAppend(b *testing.B) {
	s := NewSession("bench-concurrent", "agent")
	msg := Message{Role: "user", Content: "benchmark message", Timestamp: time.Now().Unix()}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Append(context.Background(), msg)
		}
	})
}

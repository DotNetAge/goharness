package session

import (
	"context"
	"errors"
	"testing"

	"github.com/DotNetAge/goharness/memory"
)

// ── ForceCompact 测试 ─────────────────────────────────────────────────────

func TestForceCompact_EmptyWindow_Skips(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return nil, nil
		},
	}

	s := newTestSession("fc-empty", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()),
	)

	// 无消息，活跃窗口为空
	s.ForceCompact(context.Background())

	if compactorCalled {
		t.Error("空窗口时 ForceCompact 不应调用 compactor")
	}
}

func TestForceCompact_BelowThreshold_Skips(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return nil, nil
		},
	}

	s := newTestSession("fc-below", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()),
	)

	// 少量内容，远低于 100K 阈值
	s.Append(context.Background(), Message{Role: "user", Content: "short", Timestamp: 1})

	s.ForceCompact(context.Background())

	if compactorCalled {
		t.Error("未达 100K 阈值时 ForceCompact 不应调用 compactor")
	}
}

func TestForceCompact_AboveThreshold_Triggers(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return []memory.MemoryChunk{{Summary: "摘要", Content: "压缩内容"}}, nil
		},
	}

	startCalled := false
	doneCalled := false

	s := newTestSession("fc-trigger", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()),
		WithCompactStartHandler(func(windowTokens, maxWindowSize int64) {
			startCalled = true
		}),
		WithCompactDoneHandler(func(messagesSlid int, windowTokens int64) {
			doneCalled = true
		}),
	)

	// 通过 Usage 字段构造超过 100K token 的窗口
	s.Append(context.Background(), Message{
		Role:      "assistant",
		Content:   "resp",
		Timestamp: 1,
		Usage:     &TokenUsage{CompletionTokens: 100001},
	})

	s.ForceCompact(context.Background())

	if !compactorCalled {
		t.Error("超过 100K 阈值时 ForceCompact 应调用 compactor")
	}
	if !startCalled {
		t.Error("compactStartHandler 应被调用")
	}
	if !doneCalled {
		t.Error("compactDoneHandler 应被调用")
	}
	// 压缩后 cursor 应移到末尾，活跃窗口为空
	if got := len(s.Current()); got != 0 {
		t.Errorf("ForceCompact 后活跃窗口应为空, got %d 条", got)
	}
}

func TestForceCompact_MemNil_SkipsCompactor(t *testing.T) {
	compactorCalled := false
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			compactorCalled = true
			return nil, nil
		},
	}

	// 故意不 WithMemory —— mem 保持 nil
	s := newTestSession("fc-memnil", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithCompactor(mockComp),
	)

	s.Append(context.Background(), Message{
		Role:      "assistant",
		Content:   "resp",
		Timestamp: 1,
		Usage:     &TokenUsage{CompletionTokens: 100001},
	})

	s.ForceCompact(context.Background())

	if compactorCalled {
		t.Error("mem 为 nil 时 ForceCompact 不应调用 compactor")
	}
}

func TestForceCompact_CompactorNil_SkipsSummary(t *testing.T) {
	// 未注入 compactor，但 mem 非 nil
	// ForceCompact 超过 100K 时进入 doCompact，compactor==nil 跳过摘要，但仍移动 cursor
	s := newTestSession("fc-nocomp", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithMemory(newInMemoryMemory()),
	)

	s.Append(context.Background(), Message{
		Role:      "assistant",
		Content:   "resp",
		Timestamp: 1,
		Usage:     &TokenUsage{CompletionTokens: 100001},
	})

	s.ForceCompact(context.Background())

	// compactor 为 nil 时跳过摘要生成，但不失败；cursor 仍移动（compactionFailed=false）
	if got := len(s.Current()); got != 0 {
		t.Errorf("compactor 为 nil 时 cursor 仍应移动，活跃窗口应为空, got %d 条", got)
	}
}

func TestForceCompact_CompactorError_RecordsCooldown(t *testing.T) {
	callCount := 0
	mockComp := &mockCompactor{
		CompactFunc: func(ctx context.Context, s *Session, msgs []Message) ([]memory.MemoryChunk, error) {
			callCount++
			return nil, errors.New("模拟 LLM 失败")
		},
	}

	s := newTestSession("fc-err", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 200000 }),
		WithCompactor(mockComp),
		WithMemory(newInMemoryMemory()),
	)

	s.Append(context.Background(), Message{
		Role:      "assistant",
		Content:   "resp",
		Timestamp: 1,
		Usage:     &TokenUsage{CompletionTokens: 100001},
	})

	// ForceCompact 失败应记录冷却时间戳
	s.ForceCompact(context.Background())
	if callCount != 1 {
		t.Fatalf("ForceCompact 应调用 compactor 1 次, 实际 %d", callCount)
	}

	if failNano := s.lastCompactionFailAt.Load(); failNano <= 0 {
		t.Error("ForceCompact 失败应记录冷却时间戳")
	}
}

// ── Setter 方法测试 ──────────────────────────────────────────────────────

func TestSetCompactionHandler(t *testing.T) {
	s := newTestSession("set-handler", "agent", newMockStore())
	called := false
	s.SetCompactionHandler(func(event CompactionEvent) {
		called = true
	})
	if s.compactionHandler == nil {
		t.Error("SetCompactionHandler 未设置 handler")
	}
	s.compactionHandler(CompactionEvent{})
	if !called {
		t.Error("handler 未被正确调用")
	}
}

func TestSetCompactor(t *testing.T) {
	s := newTestSession("set-compactor", "agent", newMockStore())
	mockComp := &mockCompactor{}
	s.SetCompactor(mockComp)
	if s.compactor == nil {
		t.Error("SetCompactor 未设置 compactor")
	}
}

func TestSetMemory(t *testing.T) {
	s := newTestSession("set-memory", "agent", newMockStore())
	mem := newInMemoryMemory()
	s.SetMemory(mem)
	if s.mem == nil {
		t.Error("SetMemory 未设置 memory store")
	}
}

func TestSetCompactStartHandler(t *testing.T) {
	s := newTestSession("set-cs", "agent", newMockStore())
	called := false
	s.SetCompactStartHandler(func(windowTokens, maxWindowSize int64) {
		called = true
	})
	if s.compactStartHandler == nil {
		t.Error("SetCompactStartHandler 未设置 handler")
	}
	s.compactStartHandler(100, 200)
	if !called {
		t.Error("handler 未被正确调用")
	}
}

func TestSetCompactDoneHandler(t *testing.T) {
	s := newTestSession("set-cd", "agent", newMockStore())
	called := false
	s.SetCompactDoneHandler(func(messagesSlid int, windowTokens int64) {
		called = true
	})
	if s.compactDoneHandler == nil {
		t.Error("SetCompactDoneHandler 未设置 handler")
	}
	s.compactDoneHandler(5, 100)
	if !called {
		t.Error("handler 未被正确调用")
	}
}

func TestSetMicroCompactStartHandler(t *testing.T) {
	s := newTestSession("set-mcs", "agent", newMockStore())
	called := false
	s.SetMicroCompactStartHandler(func(windowTokens, maxWindowSize int64) {
		called = true
	})
	if s.microCompactStartHandler == nil {
		t.Error("SetMicroCompactStartHandler 未设置 handler")
	}
	s.microCompactStartHandler(100, 200)
	if !called {
		t.Error("handler 未被正确调用")
	}
}

func TestSetMicroCompactDoneHandler(t *testing.T) {
	s := newTestSession("set-mcd", "agent", newMockStore())
	called := false
	s.SetMicroCompactDoneHandler(func(compressed, deduped int, windowTokens int64) {
		called = true
	})
	if s.microCompactDoneHandler == nil {
		t.Error("SetMicroCompactDoneHandler 未设置 handler")
	}
	s.microCompactDoneHandler(1, 2, 100)
	if !called {
		t.Error("handler 未被正确调用")
	}
}

// ── With* 配置选项测试（compaction 相关） ────────────────────────────────

func TestWithCompactStartHandler(t *testing.T) {
	s := newTestSession("with-cs", "agent", newMockStore(),
		WithCompactStartHandler(func(windowTokens, maxWindowSize int64) {}),
	)
	if s.compactStartHandler == nil {
		t.Error("WithCompactStartHandler 未生效")
	}
}

func TestWithCompactDoneHandler(t *testing.T) {
	s := newTestSession("with-cd", "agent", newMockStore(),
		WithCompactDoneHandler(func(messagesSlid int, windowTokens int64) {}),
	)
	if s.compactDoneHandler == nil {
		t.Error("WithCompactDoneHandler 未生效")
	}
}

// ── sanitizeMessagesForLLM 测试 ──────────────────────────────────────────

func TestSanitizeMessagesForLLM_Empty(t *testing.T) {
	out := sanitizeMessagesForLLM(nil)
	if len(out) != 0 {
		t.Errorf("空切片应返回空, got %d", len(out))
	}
}

func TestSanitizeMessagesForLLM_NoToolCalls(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	out := sanitizeMessagesForLLM(msgs)
	if len(out) != 2 {
		t.Errorf("无 tool_call 的消息应原样保留, got %d", len(out))
	}
}

func TestSanitizeMessagesForLLM_PairedToolCall(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read"}}, Content: "calling"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}
	out := sanitizeMessagesForLLM(msgs)
	if len(out) != 2 {
		t.Errorf("配对的 tool_call 应保留, got %d", len(out))
	}
}

func TestSanitizeMessagesForLLM_OrphanedToolResult(t *testing.T) {
	// assistant 有 2 个 tool_call 但只有 1 个 tool 结果 → 不完整
	// 不完整 assistant 后的 tool 消息应被移除（pendingIncomplete=true）
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "r"}, {ID: "c2", Name: "w"}}, Content: "calling"},
		{Role: "tool", ToolCallID: "c1", Content: "result1"},
	}
	out := sanitizeMessagesForLLM(msgs)
	// tool 消息应被移除（因为 assistant 不完整，pendingIncomplete=true）
	for _, m := range out {
		if m.Role == "tool" {
			t.Errorf("不完整 assistant 后的 tool 消息应被移除, got %v", m)
		}
	}
}

func TestSanitizeMessagesForLLM_IncompleteToolCall(t *testing.T) {
	// assistant 有 tool_call 但无对应 tool 结果 → 去掉 ToolCalls 保留文字
	msgs := []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read"}}, Content: "calling"},
		{Role: "assistant", Content: "next"},
	}
	out := sanitizeMessagesForLLM(msgs)
	if len(out) != 3 {
		t.Errorf("应保留 3 条消息, got %d", len(out))
	}
	// 第二条 assistant 的 ToolCalls 应被清除
	if len(out[1].ToolCalls) != 0 {
		t.Errorf("不完整的 tool_call 应被清除, got %v", out[1].ToolCalls)
	}
}

// ── needsCompaction 测试 ─────────────────────────────────────────────────

func TestNeedsCompaction_MaxWindowSizeZero(t *testing.T) {
	st := sessionState{maxWindowSize: 0}
	if st.needsCompaction() {
		t.Error("maxWindowSize 为 0 时不应需要压缩")
	}
}

func TestNeedsCompaction_EmptyActiveMessages(t *testing.T) {
	st := sessionState{maxWindowSize: 1000, activeMessages: nil}
	if st.needsCompaction() {
		t.Error("活跃窗口为空时不应需要压缩")
	}
}

func TestNeedsCompaction_BelowThreshold(t *testing.T) {
	st := sessionState{
		maxWindowSize:  1000,
		windowTokens:   700, // 70% < 80%
		activeMessages: []Message{{Role: "user", Content: "x"}},
	}
	if st.needsCompaction() {
		t.Error("低于 80% 阈值时不应需要压缩")
	}
}

func TestNeedsCompaction_AboveThreshold(t *testing.T) {
	st := sessionState{
		maxWindowSize:  1000,
		windowTokens:   850, // 85% > 80%
		activeMessages: []Message{{Role: "user", Content: "x"}},
	}
	if !st.needsCompaction() {
		t.Error("超过 80% 阈值时应需要压缩")
	}
}

// ── persistCompactionChunks 测试 ─────────────────────────────────────────

func TestPersistCompactionChunks_Empty(t *testing.T) {
	s := newTestSession("persist-empty", "agent", newMockStore())
	if err := s.persistCompactionChunks(context.Background(), nil); err != nil {
		t.Errorf("空 chunks 应返回 nil, got %v", err)
	}
}

func TestPersistCompactionChunks_MemNil(t *testing.T) {
	s := newTestSession("persist-nil", "agent", newMockStore())
	chunks := []memory.MemoryChunk{{Content: "x"}}
	err := s.persistCompactionChunks(context.Background(), chunks)
	if err == nil {
		t.Error("mem 为 nil 时应返回错误")
	}
}

func TestPersistCompactionChunks_Success(t *testing.T) {
	s := newTestSession("persist-ok", "agent", newMockStore(),
		WithMemory(newInMemoryMemory()),
	)
	chunks := []memory.MemoryChunk{{Content: "x", Summary: "s"}}
	if err := s.persistCompactionChunks(context.Background(), chunks); err != nil {
		t.Errorf("持久化应成功, got %v", err)
	}
}

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeASCIIContent 生成指定 token 估算值的 ASCII 内容。
// processContent 中 ASCII 字符 0.3 token/字符，返回 int64(t)+1。
// 因此 tokenTarget 对应的字符数约为 tokenTarget/0.3。
func makeASCIIContent(tokenTarget int) string {
	charCount := int(float64(tokenTarget) / 0.3)
	return strings.Repeat("a", charCount)
}

// ── processContent 测试 ───────────────────────────────────────────────────

func TestProcessContent_ASCII(t *testing.T) {
	content := "hello"
	sha32, tokens := processContent(content)
	if len(sha32) != 32 {
		t.Errorf("SHA32 长度 = %d, 期望 32", len(sha32))
	}
	// 5 个 ASCII 字符：0.3*5=1.5 → int64(1.5)+1 = 2
	if tokens != 2 {
		t.Errorf("ASCII token 估算 = %d, 期望 2", tokens)
	}
}

func TestProcessContent_CJK(t *testing.T) {
	content := "你好" // 2 个 CJK 字符
	_, tokens := processContent(content)
	// 2 个非 ASCII 字符：0.6*2=1.2 → int64(1.2)+1 = 2
	if tokens != 2 {
		t.Errorf("CJK token 估算 = %d, 期望 2", tokens)
	}
}

func TestProcessContent_Mixed(t *testing.T) {
	content := "a你" // 1 ASCII + 1 CJK：0.3 + 0.6 = 0.9 → 0 + 1 = 1
	_, tokens := processContent(content)
	if tokens != 1 {
		t.Errorf("混合 token 估算 = %d, 期望 1", tokens)
	}
}

func TestProcessContent_Empty(t *testing.T) {
	sha32, tokens := processContent("")
	if len(sha32) != 32 {
		t.Errorf("空内容 SHA32 长度 = %d, 期望 32", len(sha32))
	}
	// 空内容：0.0 → 0 + 1 = 1
	if tokens != 1 {
		t.Errorf("空内容 token 估算 = %d, 期望 1", tokens)
	}
}

// ── estimateWindowTokensV2 测试 ───────────────────────────────────────────

func TestEstimateWindowTokensV2_CompactedMessage(t *testing.T) {
	msgs := []Message{
		{Role: "tool", Compacted: `{"path":"/tmp/x"}`, Content: "very long content"},
	}
	tokens := estimateWindowTokensV2(msgs)
	if tokens != 20 {
		t.Errorf("Compacted 消息应贡献 20 tokens, 实际 %d", tokens)
	}
}

func TestEstimateWindowTokensV2_AssistantWithUsage(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", Content: "resp", Usage: &TokenUsage{CompletionTokens: 100, ReasoningTokens: 50}},
	}
	tokens := estimateWindowTokensV2(msgs)
	// 100 + 50 = 150
	if tokens != 150 {
		t.Errorf("Usage 消息应贡献 150 tokens, 实际 %d", tokens)
	}
}

func TestEstimateWindowTokensV2_AssistantUsageZeroFallback(t *testing.T) {
	// CompletionTokens 和 ReasoningTokens 都为 0 时，回退到字符估算
	msgs := []Message{
		{Role: "assistant", Content: "hello", Usage: &TokenUsage{}},
	}
	tokens := estimateWindowTokensV2(msgs)
	// 回退到 processContent("hello") = 2
	if tokens != 2 {
		t.Errorf("Usage 为 0 应回退到字符估算 = 2, 实际 %d", tokens)
	}
}

func TestEstimateWindowTokensV2_PlainMessage(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
	}
	tokens := estimateWindowTokensV2(msgs)
	if tokens != 2 {
		t.Errorf("普通消息 token = %d, 期望 2", tokens)
	}
}

func TestEstimateWindowTokensV2_Empty(t *testing.T) {
	if tokens := estimateWindowTokensV2(nil); tokens != 0 {
		t.Errorf("空切片 token = %d, 期望 0", tokens)
	}
}

// ── BuildToolNameByID 测试 ────────────────────────────────────────────────

func TestBuildToolNameByID_Normal(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "read"}}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-2", Name: "write"}}},
	}
	m := BuildToolNameByID(msgs)
	if m["call-1"] != "read" || m["call-2"] != "write" {
		t.Errorf("映射构建错误: %v", m)
	}
}

func TestBuildToolNameByID_SkipNonAssistant(t *testing.T) {
	msgs := []Message{
		{Role: "tool", ToolCallID: "call-1"}, // 非 assistant，应跳过
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-2", Name: "read"}}},
	}
	m := BuildToolNameByID(msgs)
	if _, ok := m["call-1"]; ok {
		t.Error("非 assistant 消息的 ToolCall 不应被收录")
	}
	if m["call-2"] != "read" {
		t.Errorf("应收录 assistant 的 ToolCall, got %v", m)
	}
}

func TestBuildToolNameByID_SkipEmptyIDOrName(t *testing.T) {
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "", Name: "read"}}},   // 空 ID 跳过
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-2", Name: ""}}}, // 空 Name 跳过
	}
	m := BuildToolNameByID(msgs)
	if len(m) != 0 {
		t.Errorf("空 ID/Name 应被跳过, got %v", m)
	}
}

func TestBuildToolNameByID_Empty(t *testing.T) {
	m := BuildToolNameByID(nil)
	if m == nil {
		t.Error("空输入应返回非 nil 的空 map")
	}
	if len(m) != 0 {
		t.Errorf("空输入应返回空 map, got %v", m)
	}
}

// ── RenderCompactedPlaceholder 测试 ───────────────────────────────────────

func TestRenderCompactedPlaceholder_Normal(t *testing.T) {
	meta := CompactedMeta{Path: "/tmp/cache.md", ToolName: "fallback-tool", TokenCount: 600}
	data, _ := json.Marshal(meta)
	msg := Message{Compacted: string(data), ToolCallID: "call-1"}

	out := RenderCompactedPlaceholder(msg, map[string]string{"call-1": "real-tool"})
	if !strings.Contains(out, "real-tool") {
		t.Errorf("应优先使用 toolNameByID 中的名称, got %q", out)
	}
	if !strings.Contains(out, "600 tokens") {
		t.Errorf("应包含 token 数, got %q", out)
	}
	if !strings.Contains(out, "/tmp/cache.md") {
		t.Errorf("应包含路径, got %q", out)
	}
}

func TestRenderCompactedPlaceholder_FallbackToolName(t *testing.T) {
	// toolNameByID 中无对应 ID，回退到 meta.ToolName
	meta := CompactedMeta{Path: "/tmp/x", ToolName: "meta-tool", TokenCount: 100}
	data, _ := json.Marshal(meta)
	msg := Message{Compacted: string(data), ToolCallID: "unknown"}

	out := RenderCompactedPlaceholder(msg, map[string]string{})
	if !strings.Contains(out, "meta-tool") {
		t.Errorf("应回退到 meta.ToolName, got %q", out)
	}
}

func TestRenderCompactedPlaceholder_CorruptedJSON(t *testing.T) {
	msg := Message{Compacted: "not-a-json"}
	out := RenderCompactedPlaceholder(msg, nil)
	if out != "not-a-json" {
		t.Errorf("损坏的 JSON 应返回原始值, got %q", out)
	}
}

// ── stripDuplicateToolMessages 测试 ──────────────────────────────────────

func TestStripDuplicateToolMessages_Empty(t *testing.T) {
	out, orphaned := stripDuplicateToolMessages(nil)
	if len(out) != 0 {
		t.Errorf("空切片应返回空, got %v", out)
	}
	if len(orphaned) != 0 {
		t.Errorf("空切片无孤立 ID, got %v", orphaned)
	}
}

func TestStripDuplicateToolMessages_Single(t *testing.T) {
	msgs := []Message{{Role: "tool", Content: "x", ToolCallID: "c1"}}
	out, orphaned := stripDuplicateToolMessages(msgs)
	if len(out) != 1 || len(orphaned) != 0 {
		t.Errorf("单条消息不应去重, out=%v orphaned=%v", out, orphaned)
	}
}

func TestStripDuplicateToolMessages_AdjacentDuplicates(t *testing.T) {
	msgs := []Message{
		{Role: "tool", Content: "same", ToolCallID: "c1"},
		{Role: "tool", Content: "same", ToolCallID: "c2"}, // 与前一条相同，应被移除
		{Role: "tool", Content: "diff", ToolCallID: "c3"},
	}
	out, orphaned := stripDuplicateToolMessages(msgs)
	if len(out) != 2 {
		t.Errorf("去重后应剩 2 条, got %d", len(out))
	}
	if !orphaned["c2"] {
		t.Errorf("被移除的 c2 应标记为孤立, got %v", orphaned)
	}
}

func TestStripDuplicateToolMessages_NonToolNotDeduped(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "same"},
		{Role: "user", Content: "same"}, // 非 tool，不去重
	}
	out, _ := stripDuplicateToolMessages(msgs)
	if len(out) != 2 {
		t.Errorf("非 tool 消息不应去重, got %d 条", len(out))
	}
}

func TestStripDuplicateToolMessages_DuplicateEmptyToolCallID(t *testing.T) {
	msgs := []Message{
		{Role: "tool", Content: "same", ToolCallID: ""},
		{Role: "tool", Content: "same", ToolCallID: ""},
	}
	out, orphaned := stripDuplicateToolMessages(msgs)
	if len(out) != 1 {
		t.Errorf("应去重 1 条, got %d", len(out))
	}
	if len(orphaned) != 0 {
		t.Errorf("空 ToolCallID 不应标记孤立, got %v", orphaned)
	}
}

// ── removeOrphanedToolCalls 测试 ─────────────────────────────────────────

func TestRemoveOrphanedToolCalls_Normal(t *testing.T) {
	s := newTestSession("rm-orphan", "agent", newMockStore())
	s.messages = []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "keep", Name: "r"}, {ID: "drop", Name: "w"}}},
		{Role: "tool", ToolCallID: "keep"},
	}
	s.removeOrphanedToolCalls(0, map[string]bool{"drop": true})
	if len(s.messages[0].ToolCalls) != 1 || s.messages[0].ToolCalls[0].ID != "keep" {
		t.Errorf("应移除孤立 ToolCall, got %v", s.messages[0].ToolCalls)
	}
}

func TestRemoveOrphanedToolCalls_NoAssistant(t *testing.T) {
	s := newTestSession("rm-orphan-2", "agent", newMockStore())
	s.messages = []Message{
		{Role: "user", Content: "x"},
		{Role: "tool", ToolCallID: "drop"},
	}
	original := len(s.messages)
	s.removeOrphanedToolCalls(0, map[string]bool{"drop": true})
	// 无 assistant，不修改
	if len(s.messages) != original {
		t.Errorf("无 assistant 时不应修改消息")
	}
}

func TestRemoveOrphanedToolCalls_EmptyOrphaned(t *testing.T) {
	s := newTestSession("rm-orphan-3", "agent", newMockStore())
	s.messages = []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "keep", Name: "r"}}},
	}
	s.removeOrphanedToolCalls(0, map[string]bool{})
	if len(s.messages[0].ToolCalls) != 1 {
		t.Errorf("空孤立集合不应移除任何 ToolCall")
	}
}

func TestRemoveOrphanedToolCalls_AllOrphaned(t *testing.T) {
	s := newTestSession("rm-orphan-4", "agent", newMockStore())
	s.messages = []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "r"}, {ID: "b", Name: "w"}}},
	}
	s.removeOrphanedToolCalls(0, map[string]bool{"a": true, "b": true})
	if len(s.messages[0].ToolCalls) != 0 {
		t.Errorf("全部孤立时应清空 ToolCalls, got %v", s.messages[0].ToolCalls)
	}
}

// ── TryMicroCompact 测试 ─────────────────────────────────────────────────

func TestTryMicroCompact_ModelContextZero_Skips(t *testing.T) {
	s := newTestSession("mc-zero", "agent", newMockStore())
	// 未配置 modelContextResolver，ModelContextLength() 返回 0
	s.messages = []Message{{Role: "user", Content: "x"}}
	if s.TryMicroCompact() {
		t.Error("ModelContextLength 为 0 时不应触发微压缩")
	}
}

func TestTryMicroCompact_EmptyWindow_Skips(t *testing.T) {
	s := newTestSession("mc-empty", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 10000 }),
	)
	// 空消息列表
	if s.TryMicroCompact() {
		t.Error("空窗口不应触发微压缩")
	}
}

func TestTryMicroCompact_BelowTrigger_Skips(t *testing.T) {
	s := newTestSession("mc-below", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 100000 }),
	)
	// 触发阈值 = 100000 * 0.45 = 45000，少量内容远低于此
	s.messages = []Message{{Role: "user", Content: "short"}}
	if s.TryMicroCompact() {
		t.Error("未达触发阈值不应触发微压缩")
	}
}

func TestTryMicroCompact_DedupOnly(t *testing.T) {
	// ModelContextLength=2000 → 触发阈值=900
	// 去重前 token=1203 > 900 触发；去重后 token=602 < 900 返回 hadDedup
	s := newTestSession("mc-dedup", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 2000 }),
	)
	// 构造相邻重复 tool 消息，去重后低于触发阈值
	dupContent := makeASCIIContent(600) // 601 token
	s.messages = []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read"}}, Timestamp: 1},
		{Role: "tool", Content: dupContent, ToolCallID: "c1", Timestamp: 2},
		{Role: "tool", Content: dupContent, ToolCallID: "c2", Timestamp: 3}, // 重复
	}
	result := s.TryMicroCompact()
	if !result {
		t.Error("发生去重时应返回 true")
	}
	// 应移除重复的 tool 消息
	if len(s.messages) != 2 {
		t.Errorf("去重后应剩 2 条消息, got %d", len(s.messages))
	}
}

func TestTryMicroCompact_Compress(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	// ModelContextLength=3000 → 触发阈值=1350, 目标=1200
	// 窗口 10 条总 token≈1810 > 1350 触发；候选范围 [2,6) 含位置 3,5 的 tool
	s := newTestSession("mc-compress", "agent", store,
		WithModelContextResolver(func() int64 { return 3000 }),
	)
	toolContent := makeASCIIContent(600) // 601 token
	s.messages = []Message{
		{Role: "user", Content: "u1", Timestamp: 1},
		{Role: "user", Content: "u2", Timestamp: 2},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read"}}, Timestamp: 3},
		{Role: "tool", Content: toolContent, ToolCallID: "t1", Timestamp: 4}, // 位置 3，候选
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t2", Name: "read"}}, Timestamp: 5},
		{Role: "tool", Content: toolContent, ToolCallID: "t2", Timestamp: 6}, // 位置 5，候选
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t3", Name: "read"}}, Timestamp: 7},
		{Role: "tool", Content: toolContent, ToolCallID: "t3", Timestamp: 8}, // 位置 7，不在范围
		{Role: "user", Content: "u3", Timestamp: 9},
		{Role: "user", Content: "u4", Timestamp: 10},
	}

	result := s.TryMicroCompact()
	if !result {
		t.Error("发生压缩时应返回 true")
	}

	// 至少有一条 tool 消息被压缩（Compacted 非空）
	compressedCount := 0
	for _, m := range s.messages {
		if m.Role == "tool" && m.Compacted != "" {
			compressedCount++
		}
	}
	if compressedCount == 0 {
		t.Error("应至少压缩一条 tool 消息")
	}

	// 验证缓存文件已写入
	cacheDir := filepath.Join(dir, "microcompact")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Errorf("缓存目录应存在: %v", err)
	}
	if len(entries) == 0 {
		t.Error("缓存目录应有缓存文件")
	}
}

func TestTryMicroCompact_NoCandidates(t *testing.T) {
	s := newTestSession("mc-nocand", "agent", newMockStore(),
		WithModelContextResolver(func() int64 { return 10000 }),
	)
	// 触发阈值 = 4500，构造大量 user 消息触发但无 tool 候选
	longContent := makeASCIIContent(600)
	msgs := make([]Message, 0, 10)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, Message{Role: "user", Content: longContent, Timestamp: int64(i + 1)})
	}
	s.messages = msgs
	// 无 tool 消息，无候选，无去重
	if s.TryMicroCompact() {
		t.Error("无候选且无去重时应返回 false")
	}
}

func TestTryMicroCompact_Handlers(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	startCalled := false
	doneCalled := false

	// ModelContextLength=2000 → 触发阈值=900, 目标=800
	// 窗口 10 条总 token≈1810 > 900 触发
	s := newTestSession("mc-handlers", "agent", store,
		WithModelContextResolver(func() int64 { return 2000 }),
		WithMicroCompactStartHandler(func(windowTokens, maxWindowSize int64) {
			startCalled = true
		}),
		WithMicroCompactDoneHandler(func(compressed, deduped int, windowTokens int64) {
			doneCalled = true
		}),
	)

	toolContent := makeASCIIContent(600)
	s.messages = []Message{
		{Role: "user", Content: "u1", Timestamp: 1},
		{Role: "user", Content: "u2", Timestamp: 2},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read"}}, Timestamp: 3},
		{Role: "tool", Content: toolContent, ToolCallID: "t1", Timestamp: 4},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t2", Name: "read"}}, Timestamp: 5},
		{Role: "tool", Content: toolContent, ToolCallID: "t2", Timestamp: 6},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t3", Name: "read"}}, Timestamp: 7},
		{Role: "tool", Content: toolContent, ToolCallID: "t3", Timestamp: 8},
		{Role: "user", Content: "u3", Timestamp: 9},
		{Role: "user", Content: "u4", Timestamp: 10},
	}

	s.TryMicroCompact()

	if !startCalled {
		t.Error("microCompactStartHandler 应被调用")
	}
	if !doneCalled {
		t.Error("microCompactDoneHandler 应被调用")
	}
}

func TestTryMicroCompact_PersistsToStore(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	// ModelContextLength=2000 → 触发阈值=900
	s := newTestSession("mc-persist", "agent", store,
		WithModelContextResolver(func() int64 { return 2000 }),
	)

	toolContent := makeASCIIContent(600)
	s.messages = []Message{
		{Role: "user", Content: "u1", Timestamp: 1},
		{Role: "user", Content: "u2", Timestamp: 2},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "read"}}, Timestamp: 3},
		{Role: "tool", Content: toolContent, ToolCallID: "t1", Timestamp: 4},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t2", Name: "read"}}, Timestamp: 5},
		{Role: "tool", Content: toolContent, ToolCallID: "t2", Timestamp: 6},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t3", Name: "read"}}, Timestamp: 7},
		{Role: "tool", Content: toolContent, ToolCallID: "t3", Timestamp: 8},
		{Role: "user", Content: "u3", Timestamp: 9},
		{Role: "user", Content: "u4", Timestamp: 10},
	}

	s.TryMicroCompact()

	// 验证 store 收到 UpdateMessages 调用
	stored, ok := store.msgs[s.id]
	if !ok {
		t.Fatal("压缩后应持久化消息到 store")
	}
	if len(stored) != len(s.messages) {
		t.Errorf("store 中消息数 = %d, 期望 %d", len(stored), len(s.messages))
	}
}

// ── 微压缩配置选项测试 ────────────────────────────────────────────────────

func TestWithMicroCompactHandlers(t *testing.T) {
	startCalled := false
	doneCalled := false
	s := newTestSession("mc-opts", "agent", newMockStore(),
		WithMicroCompactStartHandler(func(windowTokens, maxWindowSize int64) {
			startCalled = true
		}),
		WithMicroCompactDoneHandler(func(compressed, deduped int, windowTokens int64) {
			doneCalled = true
		}),
	)
	if s.microCompactStartHandler == nil {
		t.Error("WithMicroCompactStartHandler 未生效")
	}
	if s.microCompactDoneHandler == nil {
		t.Error("WithMicroCompactDoneHandler 未生效")
	}
	// 触发一次以验证可调用
	s.microCompactStartHandler(100, 200)
	s.microCompactDoneHandler(1, 0, 50)
	if !startCalled || !doneCalled {
		t.Error("handler 未被正确调用")
	}
}

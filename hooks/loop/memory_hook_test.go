package loop

import (
	"errors"
	"strings"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
)

// ── 构造与基础方法测试 ───────────────────────────────────────────────────

func TestNewMemoryThoughtHook(t *testing.T) {
	mem := &mockMemory{}
	h := NewMemoryThoughtHook(mem)
	if h == nil {
		t.Fatal("NewMemoryThoughtHook 不应返回 nil")
	}
	if h.memory == nil {
		t.Error("构造后 memory 字段不应为 nil")
	}
}

func TestMemoryThoughtHook_Priority(t *testing.T) {
	h := NewMemoryThoughtHook(&mockMemory{})
	if h.Priority() != 50 {
		t.Errorf("Priority = %d, 期望 50", h.Priority())
	}
}

func TestMemoryThoughtHook_AfterLLM(t *testing.T) {
	h := NewMemoryThoughtHook(&mockMemory{})
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, nil)
	if result.Abort {
		t.Error("AfterLLM 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_Abort(t *testing.T) {
	h := NewMemoryThoughtHook(&mockMemory{})
	// 应不 panic
	h.Abort("s1", "测试原因")
}

// ── BeforeLLM 跳过场景测试 ──────────────────────────────────────────────

func TestMemoryThoughtHook_BeforeLLM_NilMemory(t *testing.T) {
	h := NewMemoryThoughtHook(nil)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent"}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("nil memory 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_EmptyAgentName(t *testing.T) {
	h := NewMemoryThoughtHook(&mockMemory{})
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: ""}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("空 AgentName 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_AlreadyInjected(t *testing.T) {
	mem := &mockMemory{
		LatestResult: []memory.MemoryChunk{{Content: "已有记忆"}},
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()

	// SystemPromptSections 末尾已包含"## 历史对话摘要"
	input := &hooks.CallInput{
		AgentName: "agent",
		SystemPromptSections: []gochatcore.Message{
			{Role: "system", Content: []gochatcore.ContentBlock{
				{Type: "text", Text: "## 历史对话摘要\n已有内容"},
			}},
		},
	}
	h.BeforeLLM("s1", 0, input)

	// 应跳过，不调用 RetrieveLatest
	if mem.LatestAgent != "" {
		t.Error("已注入记忆时应跳过，不调用 RetrieveLatest")
	}
}

func TestMemoryThoughtHook_BeforeLLM_NotLatestRetriever(t *testing.T) {
	h := NewMemoryThoughtHook(&mockMemoryNoLatest{})
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent"}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("不支持 LatestRetriever 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_RetrieveLatestError(t *testing.T) {
	mem := &mockMemory{
		LatestError: errors.New("检索失败"),
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent", ProjectDir: "/tmp"}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("RetrieveLatest 失败不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_EmptyResults_NoSessionID(t *testing.T) {
	mem := &mockMemory{LatestResult: nil}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent"}
	// sessionID 为空，不触发回退
	result := h.BeforeLLM("", 0, input)
	if result.Abort {
		t.Error("空结果无 sessionID 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_EmptyResults_FallbackSession(t *testing.T) {
	mem := &mockMemory{
		LatestResult:  nil, // LatestRetriever 返回空
		SessionResult: []memory.MemoryChunk{{Content: "会话记忆"}},
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent", ProjectDir: "/tmp"}

	h.BeforeLLM("session-123", 0, input)

	// 应回退到 RetrieveBySession
	if mem.SessionIDCalled != "session-123" {
		t.Errorf("应回退调用 RetrieveBySession, got SessionIDCalled=%q", mem.SessionIDCalled)
	}
}

func TestMemoryThoughtHook_BeforeLLM_FallbackSessionError(t *testing.T) {
	mem := &mockMemory{
		LatestResult: nil,
		SessionError: errors.New("会话检索失败"),
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent", ProjectDir: "/tmp"}

	result := h.BeforeLLM("session-123", 0, input)
	if result.Abort {
		t.Error("会话回退失败不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_EmptyResults_FallbackAlsoEmpty(t *testing.T) {
	mem := &mockMemory{
		LatestResult:  nil,
		SessionResult: nil,
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{AgentName: "agent", ProjectDir: "/tmp"}

	result := h.BeforeLLM("session-123", 0, input)
	if result.Abort {
		t.Error("回退也空不应返回 Abort")
	}
}

// ── BeforeLLM 正常注入测试 ──────────────────────────────────────────────

func TestMemoryThoughtHook_BeforeLLM_NormalInjection(t *testing.T) {
	mem := &mockMemory{
		LatestResult: []memory.MemoryChunk{
			{Content: "最新记忆", Summary: "摘要1"},
			{Content: "较早记忆", Summary: "摘要2"},
		},
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{
		AgentName:  "test-agent",
		ProjectDir: "/tmp/project",
		SystemPromptSections: []gochatcore.Message{
			{Role: "system", Content: []gochatcore.ContentBlock{
				{Type: "text", Text: "系统指令"},
			}},
		},
	}

	h.BeforeLLM("s1", 0, input)

	// 验证 RetrieveLatest 被正确调用
	if mem.LatestAgent != "test-agent" {
		t.Errorf("LatestAgent = %q, 期望 test-agent", mem.LatestAgent)
	}
	if mem.LatestProject != "/tmp/project" {
		t.Errorf("LatestProject = %q, 期望 /tmp/project", mem.LatestProject)
	}
	if mem.LatestLimit != 20 {
		t.Errorf("LatestLimit = %d, 期望 20", mem.LatestLimit)
	}

	// 验证记忆被注入到 SystemPromptSections 末尾
	if len(input.SystemPromptSections) == 0 {
		t.Fatal("SystemPromptSections 不应为空")
	}
	last := input.SystemPromptSections[len(input.SystemPromptSections)-1]
	if len(last.Content) < 2 {
		t.Fatalf("应追加记忆内容块, got %d blocks", len(last.Content))
	}
	memBlock := last.Content[len(last.Content)-1]
	if !strings.Contains(memBlock.Text, "## 历史对话摘要") {
		t.Errorf("注入内容应包含标题, got %q", memBlock.Text)
	}
	// 记忆应按时间正序（旧至新）注入：较早记忆在前，最新记忆在后
	if !strings.Contains(memBlock.Text, "较早记忆") || !strings.Contains(memBlock.Text, "最新记忆") {
		t.Errorf("注入内容应包含所有记忆, got %q", memBlock.Text)
	}
}

func TestMemoryThoughtHook_BeforeLLM_NoSystemSections(t *testing.T) {
	mem := &mockMemory{
		LatestResult: []memory.MemoryChunk{{Content: "记忆"}},
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	// SystemPromptSections 为空 —— 不注入但仍正常返回
	input := &hooks.CallInput{AgentName: "agent"}

	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("无 SystemPromptSections 不应返回 Abort")
	}
}

func TestMemoryThoughtHook_BeforeLLM_EmptyFieldsStillInjects(t *testing.T) {
	// MemoryChunk 的 Content 和 Summary 都为空时，FormatMemoryRecords 仍返回非空
	// （至少输出 "- [] " 格式），因此仍会注入
	mem := &mockMemory{
		LatestResult: []memory.MemoryChunk{{Content: "", Summary: ""}},
	}
	h := NewMemoryThoughtHook(mem)
	h.Logger = logging.NewNopLogger()
	input := &hooks.CallInput{
		AgentName: "agent",
		SystemPromptSections: []gochatcore.Message{
			{Role: "system", Content: []gochatcore.ContentBlock{{Type: "text", Text: "原始"}}},
		},
	}

	h.BeforeLLM("s1", 0, input)

	// 即使字段为空，FormatMemoryRecords 返回非空，仍应注入
	last := input.SystemPromptSections[len(input.SystemPromptSections)-1]
	if len(last.Content) != 2 {
		t.Errorf("空字段 chunk 仍应注入, got %d blocks", len(last.Content))
	}
}

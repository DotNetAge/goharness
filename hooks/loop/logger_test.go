package loop

import (
	"testing"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// ── LoopLoggerHook 基础方法测试 ──────────────────────────────────────────

func TestLoopLoggerHook_Priority(t *testing.T) {
	h := &LoopLoggerHook{}
	if h.Priority() != hooks.PriorityLoopLogger {
		t.Errorf("Priority = %d, 期望 %d", h.Priority(), hooks.PriorityLoopLogger)
	}
}

func TestLoopLoggerHook_Abort(t *testing.T) {
	h := &LoopLoggerHook{}
	h.Abort("s1", "测试") // 应不 panic
}

// ── BeforeLLM 测试 ───────────────────────────────────────────────────────

func TestLoopLoggerHook_BeforeLLM_NilLogger(t *testing.T) {
	h := &LoopLoggerHook{Logger: nil}
	input := &hooks.CallInput{UserMessage: "hello"}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("nil Logger 不应返回 Abort")
	}
}

func TestLoopLoggerHook_BeforeLLM_Normal(t *testing.T) {
	h := &LoopLoggerHook{Logger: logging.NewNopLogger()}
	input := &hooks.CallInput{UserMessage: "这是一条测试消息"}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("正常调用不应返回 Abort")
	}
}

func TestLoopLoggerHook_BeforeLLM_LongMessage(t *testing.T) {
	h := &LoopLoggerHook{Logger: logging.NewNopLogger()}
	// 超长消息应被截断，不 panic
	longMsg := ""
	for i := 0; i < 200; i++ {
		longMsg += "x"
	}
	input := &hooks.CallInput{UserMessage: longMsg}
	result := h.BeforeLLM("s1", 0, input)
	if result.Abort {
		t.Error("超长消息不应返回 Abort")
	}
}

// ── AfterLLM 测试 ────────────────────────────────────────────────────────

func TestLoopLoggerHook_AfterLLM_NilLogger(t *testing.T) {
	h := &LoopLoggerHook{Logger: nil}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, nil)
	if result.Abort {
		t.Error("nil Logger 不应返回 Abort")
	}
}

func TestLoopLoggerHook_AfterLLM_WithTools(t *testing.T) {
	h := &LoopLoggerHook{Logger: logging.NewNopLogger()}
	resp := &hooks.LLMResponse{
		ToolCalls: []hooks.ToolCallInvocation{
			{ID: "c1", Name: "read"},
			{ID: "c2", Name: "write"},
		},
	}
	result := h.AfterLLM("s1", 0, resp, nil)
	if result.Abort {
		t.Error("有 tool calls 不应返回 Abort")
	}
}

func TestLoopLoggerHook_AfterLLM_NoTools(t *testing.T) {
	h := &LoopLoggerHook{Logger: logging.NewNopLogger()}
	resp := &hooks.LLMResponse{ToolCalls: nil}
	result := h.AfterLLM("s1", 0, resp, nil)
	if result.Abort {
		t.Error("无 tool calls 不应返回 Abort")
	}
}

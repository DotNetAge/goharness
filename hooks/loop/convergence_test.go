package loop

import (
	"testing"

	"github.com/DotNetAge/goharness/hooks"
)

// ── ConvergenceHook 基础方法测试 ─────────────────────────────────────────

func TestConvergenceHook_Priority(t *testing.T) {
	h := &ConvergenceHook{}
	if h.Priority() != hooks.PriorityConvergence {
		t.Errorf("Priority = %d, 期望 %d", h.Priority(), hooks.PriorityConvergence)
	}
}

func TestConvergenceHook_BeforeLLM(t *testing.T) {
	h := &ConvergenceHook{}
	result := h.BeforeLLM("s1", 0, &hooks.CallInput{})
	if result.Abort {
		t.Error("BeforeLLM 不应返回 Abort")
	}
}

func TestConvergenceHook_Abort(t *testing.T) {
	h := &ConvergenceHook{}
	h.Abort("s1", "测试") // 应不 panic
}

// ── AfterLLM 测试 ────────────────────────────────────────────────────────

func TestConvergenceHook_AfterLLM_NoErrors(t *testing.T) {
	h := &ConvergenceHook{}
	results := []hooks.ToolResult{
		{ToolName: "read", Success: true, Result: "ok"},
	}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, results)
	if result.Abort {
		t.Error("无错误时不应返回 Abort")
	}
}

func TestConvergenceHook_AfterLLM_RecoverableError(t *testing.T) {
	h := &ConvergenceHook{}
	results := []hooks.ToolResult{
		{ToolName: "read", Success: false, Error: "文件不存在"},
	}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, results)
	if result.Abort {
		t.Error("可恢复错误不应返回 Abort")
	}
}

func TestConvergenceHook_AfterLLM_IrrecoverableError(t *testing.T) {
	h := &ConvergenceHook{}
	results := []hooks.ToolResult{
		{ToolName: "exec", Success: false, Error: "permission denied"},
	}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, results)
	if !result.Abort {
		t.Error("不可恢复错误应返回 Abort")
	}
	if result.AbortReason == "" {
		t.Error("AbortReason 不应为空")
	}
}

func TestConvergenceHook_AfterLLM_MultipleResults_FirstIrrecoverable(t *testing.T) {
	h := &ConvergenceHook{}
	results := []hooks.ToolResult{
		{ToolName: "read", Success: true, Result: "ok"},
		{ToolName: "exec", Success: false, Error: "unauthorized access"},
		{ToolName: "write", Success: true, Result: "done"},
	}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, results)
	if !result.Abort {
		t.Error("含不可恢复错误时应返回 Abort")
	}
}

func TestConvergenceHook_AfterLLM_SuccessWithErrorEmpty(t *testing.T) {
	h := &ConvergenceHook{}
	// Success=false 但 Error 为空，不应触发 Abort
	results := []hooks.ToolResult{
		{ToolName: "read", Success: false, Error: ""},
	}
	result := h.AfterLLM("s1", 0, &hooks.LLMResponse{}, results)
	if result.Abort {
		t.Error("Error 为空时不应返回 Abort")
	}
}

// ── isIrrecoverable 测试 ─────────────────────────────────────────────────

func TestIsIrrecoverable_Empty(t *testing.T) {
	if isIrrecoverable("") {
		t.Error("空字符串不应为不可恢复")
	}
}

func TestIsIrrecoverable_NoMatch(t *testing.T) {
	if isIrrecoverable("文件不存在") {
		t.Error("不匹配模式应为可恢复")
	}
}

func TestIsIrrecoverable_PermissionDenied(t *testing.T) {
	if !isIrrecoverable("permission denied") {
		t.Error("permission denied 应为不可恢复")
	}
}

func TestIsIrrecoverable_CaseInsensitive(t *testing.T) {
	if !isIrrecoverable("PERMISSION DENIED") {
		t.Error("大小写不敏感匹配应成功")
	}
	if !isIrrecoverable("Permission Denied") {
		t.Error("混合大小写匹配应成功")
	}
}

func TestIsIrrecoverable_AllPatterns(t *testing.T) {
	patterns := []string{
		"permission denied",
		"unauthorized",
		"invalid api key",
		"authentication failed",
		"access denied",
		"forbidden",
	}
	for _, p := range patterns {
		if !isIrrecoverable(p) {
			t.Errorf("模式 %q 应为不可恢复", p)
		}
	}
}

func TestIsIrrecoverable_PartialMatch(t *testing.T) {
	if !isIrrecoverable("操作失败: permission denied for /etc") {
		t.Error("部分匹配应为不可恢复")
	}
}

// ── containsLower 测试 ───────────────────────────────────────────────────

func TestContainsLower_EmptySubstr(t *testing.T) {
	// 空子字符串：len(s) >= 0 恒真，循环 i=0, match=true (内层不执行), return true
	if !containsLower("abc", "") {
		t.Error("空子字符串应匹配")
	}
}

func TestContainsLower_ShorterString(t *testing.T) {
	if containsLower("ab", "abc") {
		t.Error("短字符串不应匹配长子字符串")
	}
}

func TestContainsLower_ExactMatch(t *testing.T) {
	if !containsLower("hello", "hello") {
		t.Error("完全匹配应成功")
	}
}

func TestContainsLower_SubstringMatch(t *testing.T) {
	if !containsLower("say hello world", "hello") {
		t.Error("子字符串匹配应成功")
	}
}

func TestContainsLower_CaseInsensitive(t *testing.T) {
	if !containsLower("HELLO", "hello") {
		t.Error("大写转小写应匹配")
	}
	if !containsLower("HeLLo", "hello") {
		t.Error("混合大小写应匹配")
	}
}

func TestContainsLower_NoMatch(t *testing.T) {
	if containsLower("world", "hello") {
		t.Error("不匹配应返回 false")
	}
}

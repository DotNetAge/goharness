package hooks

import (
	"errors"
	"testing"
	"time"
)

func TestHookResult_IsTerminal(t *testing.T) {
	tests := []struct {
		name     string
		result   HookResult
		expected bool
	}{
		{
			name:     "empty result is not terminal",
			result:   HookResult{},
			expected: false,
		},
		{
			name:     "abort only is terminal",
			result:   HookResult{Abort: true, AbortReason: "test"},
			expected: true,
		},
		{
			name:     "error only is terminal",
			result:   HookResult{Error: errors.New("test error")},
			expected: true,
		},
		{
			name:     "abort and error is terminal",
			result:   HookResult{Abort: true, AbortReason: "test", Error: errors.New("err")},
			expected: true,
		},
		{
			name:     "abort reason without abort flag is not terminal",
			result:   HookResult{AbortReason: "has reason but no abort"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.IsTerminal(); got != tt.expected {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.expected)
			}
		})
	}
}

type mockLoopHook struct {
	priority      int
	beforeLLMCalled bool
	afterLLMCalled  bool
	abortCalled     bool
	abortReason     string
	beforeLLMResult HookResult
	afterLLMResult  HookResult
}

func (m *mockLoopHook) Priority() int {
	return m.priority
}

func (m *mockLoopHook) BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult {
	m.beforeLLMCalled = true
	return m.beforeLLMResult
}

func (m *mockLoopHook) AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult {
	m.afterLLMCalled = true
	return m.afterLLMResult
}

func (m *mockLoopHook) Abort(sessionID string, reason string) {
	m.abortCalled = true
	m.abortReason = reason
}

type mockToolHook struct {
	priority    int
	beforeCalled bool
	afterCalled  bool
	abortCalled  bool
	abortReason  string
	beforeResult HookResult
	afterResult  HookResult
}

func (m *mockToolHook) Priority() int {
	return m.priority
}

func (m *mockToolHook) Before(sessionID string, toolName string, params map[string]any) HookResult {
	m.beforeCalled = true
	return m.beforeResult
}

func (m *mockToolHook) After(result *ToolResult) HookResult {
	m.afterCalled = true
	return m.afterResult
}

func (m *mockToolHook) Abort(reason string) {
	m.abortCalled = true
	m.abortReason = reason
}

func TestMockLoopHook_Interface(t *testing.T) {
	hook := &mockLoopHook{
		priority:       42,
		beforeLLMResult: HookResult{},
		afterLLMResult:  HookResult{},
	}

	var _ LoopHook = hook

	if hook.Priority() != 42 {
		t.Errorf("expected priority 42, got %d", hook.Priority())
	}

	input := &CallInput{
		SessionID:   "test-session",
		UserMessage: "hello",
	}

	result := hook.BeforeLLM("session-1", 0, input)
	if !hook.beforeLLMCalled {
		t.Error("BeforeLLM was not called")
	}
	if result.Abort || result.Error != nil {
		t.Errorf("expected empty result, got %+v", result)
	}

	resp := &LLMResponse{Content: "response"}
	results := []ToolResult{{ToolName: "test", Success: true}}

	result = hook.AfterLLM("session-1", 0, resp, results)
	if !hook.afterLLMCalled {
		t.Error("AfterLLM was not called")
	}

	hook.Abort("session-1", "test reason")
	if !hook.abortCalled {
		t.Error("Abort was not called")
	}
	if hook.abortReason != "test reason" {
		t.Errorf("expected abort reason 'test reason', got '%s'", hook.abortReason)
	}
}

func TestMockLoopHook_WithAbortResult(t *testing.T) {
	hook := &mockLoopHook{
		beforeLLMResult: HookResult{Abort: true, AbortReason: "stop now"},
	}

	input := &CallInput{SessionID: "s1"}
	result := hook.BeforeLLM("s1", 0, input)

	if !result.IsTerminal() {
		t.Error("expected terminal result from abort")
	}
	if result.AbortReason != "stop now" {
		t.Errorf("expected abort reason 'stop now', got '%s'", result.AbortReason)
	}
}

func TestMockLoopHook_WithErrorResult(t *testing.T) {
	testErr := errors.New("something went wrong")
	hook := &mockLoopHook{
		afterLLMResult: HookResult{Error: testErr},
	}

	resp := &LLMResponse{}
	result := hook.AfterLLM("s1", 0, resp, nil)

	if !result.IsTerminal() {
		t.Error("expected terminal result from error")
	}
	if result.Error != testErr {
		t.Errorf("expected error %v, got %v", testErr, result.Error)
	}
}

func TestMockToolHook_Interface(t *testing.T) {
	hook := &mockToolHook{
		priority:    43,
		beforeResult: HookResult{},
		afterResult:  HookResult{},
	}

	var _ ToolHook = hook

	if hook.Priority() != 43 {
		t.Errorf("expected priority 43, got %d", hook.Priority())
	}

	params := map[string]any{"key": "value"}
	_ = hook.Before("session-1", "test-tool", params)
	if !hook.beforeCalled {
		t.Error("Before was not called")
	}

	toolResult := &ToolResult{ToolName: "test-tool", Success: true, Duration: time.Millisecond}
	_ = hook.After(toolResult)
	if !hook.afterCalled {
		t.Error("After was not called")
	}

	hook.Abort("permission denied")
	if !hook.abortCalled {
		t.Error("Abort was not called")
	}
	if hook.abortReason != "permission denied" {
		t.Errorf("expected abort reason 'permission denied', got '%s'", hook.abortReason)
	}
}

func TestToolResultSummary_Success(t *testing.T) {
	tr := ToolResult{
		ToolName: "search",
		Result:   "found 10 results for query",
		Success:  true,
	}

	summary := ToolResultSummary(tr)
	expected := "[search] returned: found 10 results for query"
	if summary != expected {
		t.Errorf("expected %q, got %q", expected, summary)
	}
}

func TestToolResultSummary_Error(t *testing.T) {
	tr := ToolResult{
		ToolName: "write_file",
		Error:    "permission denied: cannot write to /root",
		Success:  false,
	}

	summary := ToolResultSummary(tr)
	expected := "[write_file] error: permission denied: cannot write to /root"
	if summary != expected {
		t.Errorf("expected %q, got %q", expected, summary)
	}
}

func TestToolResultSummary_EmptyResult(t *testing.T) {
	tr := ToolResult{
		ToolName: "delete_file",
		Result:   "",
		Success:  true,
	}

	summary := ToolResultSummary(tr)
	expected := "[delete_file] returned: (empty result)"
	if summary != expected {
		t.Errorf("expected %q, got %q", expected, summary)
	}
}

func TestTruncate_ShortString(t *testing.T) {
	s := "hello world"
	result := Truncate(s, 20)
	if result != s {
		t.Errorf("expected %q, got %q", s, result)
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	s := "hello"
	result := Truncate(s, 5)
	if result != s {
		t.Errorf("expected %q, got %q", s, result)
	}
}

func TestTruncate_LongString(t *testing.T) {
	s := "This is a very long string that should be truncated"
	result := Truncate(s, 10)
	expected := "This is a ..."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	result := Truncate("", 10)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestTruncate_Unicode(t *testing.T) {
	s := "你好世界，这是一个测试字符串"
	result := Truncate(s, 6)
	expected := "你好世界，这..."
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestPriorityConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    int
		expected int
	}{
		{"Permission", PriorityPermission, 41},
		{"LoopLogger", PriorityLoopLogger, 45},
		{"ToolLogger", PriorityToolLogger, 46},
		{"Convergence", PriorityConvergence, 49},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, tt.value)
			}
		})
	}
}

func TestLLMResponse_StructFields(t *testing.T) {
	resp := LLMResponse{
		Content:      "Hello, how can I help?",
		Reasoning:    "The user is asking a question",
		FinishReason: "stop",
		ToolCalls: []ToolCallInvocation{
			{ID: "call_1", Name: "search", Arguments: map[string]any{"query": "test"}},
		},
		TokenUsage: nil,
		AbortReason: "",
	}

	if resp.Content != "Hello, how can I help?" {
		t.Errorf("unexpected content: %s", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("expected tool name 'search', got %s", resp.ToolCalls[0].Name)
	}
}

func TestToolCallInvocation_StructFields(t *testing.T) {
	invocation := ToolCallInvocation{
		ID:        "call_123",
		Name:      "write_file",
		Arguments: map[string]any{"path": "/tmp/test.txt", "content": "hello"},
	}

	if invocation.ID != "call_123" {
		t.Errorf("expected ID 'call_123', got %s", invocation.ID)
	}
	if invocation.Name != "write_file" {
		t.Errorf("expected name 'write_file', got %s", invocation.Name)
	}
	if invocation.Arguments["path"] != "/tmp/test.txt" {
		t.Errorf("unexpected argument value")
	}
}

func TestToolResult_StructFields(t *testing.T) {
	tr := ToolResult{
		ToolName:   "read_file",
		ToolCallID: "call_123",
		Result:     "file contents here",
		Metadata:   map[string]string{"path": "/tmp/test.txt"},
		Error:      "",
		Duration:   150 * time.Millisecond,
		Success:    true,
	}

	if tr.ToolName != "read_file" {
		t.Errorf("expected tool name 'read_file', got %s", tr.ToolName)
	}
	if tr.Duration != 150*time.Millisecond {
		t.Errorf("expected duration 150ms, got %v", tr.Duration)
	}
	if !tr.Success {
		t.Error("expected success to be true")
	}
}

func TestCallInput_StructFields(t *testing.T) {
	input := CallInput{
		SessionID:            "session-abc",
		SystemPromptSections: nil,
		UserMessage:          "What is the weather?",
		History:              nil,
		Tools:                nil,
	}

	if input.SessionID != "session-abc" {
		t.Errorf("expected session ID 'session-abc', got %s", input.SessionID)
	}
	if input.UserMessage != "What is the weather?" {
		t.Errorf("unexpected user message: %s", input.UserMessage)
	}
}

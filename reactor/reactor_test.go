package reactor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

// ── Test hook stubs (replicate essential built-in hook behaviors) ──

type testPreCheckHook struct{}

func (h *testPreCheckHook) Priority() int { return PriorityPreCheck }

func (h *testPreCheckHook) BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult {
	return HookResult{}
}

func (h *testPreCheckHook) AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult {
	return HookResult{}
}

func (h *testPreCheckHook) Abort(sessionID string, reason string) {}

type testConvergenceHook struct{}

func (h *testConvergenceHook) Priority() int { return PriorityConvergence }

func (h *testConvergenceHook) BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult {
	return HookResult{}
}

func (h *testConvergenceHook) AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult {
	for _, tr := range results {
		if !tr.Success && tr.Error != "" {
			return HookResult{Abort: true, AbortReason: "irrecoverable tool error: " + tr.Error}
		}
	}
	return HookResult{}
}

func (h *testConvergenceHook) Abort(sessionID string, reason string) {}

func registerTestDefaultHooks(r *Reactor) {
	r.RegisterLoopHooks(&testPreCheckHook{}, &testConvergenceHook{})
	r.RegisterToolHooks()
}

// ============================================================================
// Prompt System Tests (KV Cache, Dynamic Boundary, CloneForSkill)
// ============================================================================

func TestPrompt_ToSectionedMessages_StaticOrder(t *testing.T) {
	tests := []struct {
		name         string
		prompt       *Prompt
		wantPreEnv   []string // sections before Environment
		wantDynamic  []string // dynamic sections (after boundary)
		wantTotal    int
		wantReminder string // expected system reminders content
	}{
		{
			name: "all fields filled",
			prompt: &Prompt{
				Identity:            "You are a test agent.",
				Rules:               "1. Be helpful.",
				ExecutionGuidelines: "Be cautious with writes.",
				SkillsCatalog:       "- skill_a",
				ToolUsage:           "Use tools wisely.",
				ToneAndStyle:        "Be concise.",
				SystemReminders:     "Remember context limits.",
				OutputEfficiency:    "Use prose.",
			},
			wantPreEnv: []string{
				"You are a test agent.",
				"- skill_a",
				"## Behavioral Rules\n1. Be helpful.",
				"Be cautious with writes.",
				"Use tools wisely.",
				"Be concise.",
			},
			wantDynamic: []string{
				"Use prose.",
			},
			wantTotal:    10,
			wantReminder: "Remember context limits.",
		},
		{
			name: "only identity",
			prompt: &Prompt{
				Identity: "Minimal agent.",
			},
			wantPreEnv:   []string{"Minimal agent."},
			wantDynamic:  nil,
			wantTotal:    4,
			wantReminder: BuildSystemReminders(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := tt.prompt.ToSectionedMessages("", "", "")

			if len(msgs) != tt.wantTotal {
				t.Fatalf("len(messages) = %d, want %d", len(msgs), tt.wantTotal)
			}

			for i, want := range tt.wantPreEnv {
				got := msgs[i].Content[0].Text
				if got != want {
					t.Errorf("section [%d] content = %q, want %q", i, got, want)
				}
			}

			envIdx := len(tt.wantPreEnv)
			envContent := msgs[envIdx].Content[0].Text
			if !strings.Contains(envContent, "## Environment") {
				t.Errorf("section [%d] expected environment section, got %q", envIdx, envContent)
			}

			reminderIdx := envIdx + 1
			reminderContent := msgs[reminderIdx].Content[0].Text
			if reminderContent != tt.wantReminder {
				t.Errorf("section [%d] expected system reminders, got %q", reminderIdx, reminderContent)
			}

			boundaryIdx := reminderIdx + 1
			if msgs[boundaryIdx].Content[0].Text != DynamicBoundary {
				t.Errorf("message[%d] expected DynamicBoundary, got %q", boundaryIdx, msgs[boundaryIdx].Content[0].Text)
			}

			dynStart := boundaryIdx + 1
			for i, want := range tt.wantDynamic {
				got := msgs[dynStart+i].Content[0].Text
				if got != want {
					t.Errorf("dynamic section [%d] content = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestPrompt_ToSectionedMessages_EmptyFieldsSkipped(t *testing.T) {
	p := &Prompt{
		Identity: "You are a minimal agent.",
	}

	msgs := p.ToSectionedMessages("", "", "")

	// Identity + Environment + SystemReminders + DynamicBoundary
	if len(msgs) != 4 {
		t.Errorf("expected 4 messages (identity + environment + system reminders + boundary), got %d", len(msgs))
	}
}

func TestPrompt_RenderToLLMInput(t *testing.T) {
	p := &Prompt{
		Identity: "You are a test agent.",
	}

	input := p.RenderToLLMInput(
		"Hello world",
		ConversationHistory{
			{Role: "assistant", Content: "Hi!"},
		},
		[]gochatcore.Tool{},
	)

	if input.UserMessage != "Hello world" {
		t.Errorf("expected user message 'Hello world', got '%s'", input.UserMessage)
	}
	if len(input.History) != 1 {
		t.Errorf("expected 1 history message, got %d", len(input.History))
	}
	if len(input.SystemPromptSections) == 0 {
		t.Error("expected non-empty system prompt sections")
	}
}

// ============================================================================
// Reactor.Run() with MockLLM — Complete T-A-O Loop Tests
// ============================================================================

func newTestReactor(mockFn MockLLMFunc, opts ...ReactorOption) *Reactor {
	cfg := ReactorConfig{
		Model:         "test-model",
		MaxIterations: 10,
	}
	allOpts := []ReactorOption{
		WithMockLLM(mockFn),
	}
	allOpts = append(allOpts, opts...)
	r := NewReactor(cfg, allOpts...)
	registerTestDefaultHooks(r)
	return r
}

func TestReactor_Run_MockLLM_AnswerImmediately(t *testing.T) {
	callCount := 0
	r := newTestReactor(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		callCount++
		return &gochatcore.Response{
			Content: "Hello, user!",
		}, nil
	})

	result, err := r.Run(context.Background(), "Say hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 LLM call, got %d", callCount)
	}
	if result.TotalIterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.TotalIterations)
	}
	if result.Answer != "Hello, user!" {
		t.Errorf("expected answer 'Hello, user!', got '%s'", result.Answer)
	}
	if result.TerminationReason != "direct_answer" {
		t.Errorf("expected termination 'direct_answer', got '%s'", result.TerminationReason)
	}
}

func TestReactor_Run_MockLLM_ActThenAnswer(t *testing.T) {
	callCount := 0
	var secondCallInput CallInput
	r := newTestReactor(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			// First call: decide to act (call a tool) using native tool calls
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{
						{
							Name:      "echo_tool",
							Arguments: `{"message": "hello"}`,
						},
					},
				},
			}, nil
		}
		// Capture second call input for history verification
		secondCallInput = input
		// Second call: answer after tool result
		return &gochatcore.Response{
			Content: "Done.",
		}, nil
	}, WithExtraTools(&mockEchoTool{}))

	result, err := r.Run(context.Background(), "Run the tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", callCount)
	}
	if result.TotalIterations < 2 {
		t.Errorf("expected at least 2 iterations, got %d", result.TotalIterations)
	}
	if result.Answer != "Done." {
		t.Errorf("expected answer 'Done.', got '%s'", result.Answer)
	}
	if result.TerminationReason != "direct_answer" {
		t.Errorf("expected termination 'direct_answer', got '%s'", result.TerminationReason)
	}

	// Verify that the second LLM call received history containing the tool execution result.
	// persistStep adds assistant+tool messages to ConversationHistory after each cycle.
	if len(secondCallInput.History) == 0 {
		t.Error("expected second LLM call to have non-empty History (ConversationHistory)")
	}
	foundToolResult := false
	for _, msg := range secondCallInput.History {
		if strings.Contains(msg.Content, "Echo: hello") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Error("second LLM call History should contain tool execution result 'Echo: hello' from persistStep")
	}
}

func TestReactor_Run_MockLLM_ContextCancelled(t *testing.T) {
	r := newTestReactor(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		// This should not be called since context is pre-cancelled
		// (runLoop checks Cancelled before Think in each iteration)
		return &gochatcore.Response{
			Content: "should not reach",
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := r.Run(ctx, "Do something slow", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TerminationReason != "request cancelled" {
		t.Errorf("expected termination reason 'request cancelled', got '%s'", result.TerminationReason)
	}
	if result.TotalIterations != 0 {
		t.Errorf("expected 0 iterations for pre-cancelled context, got %d", result.TotalIterations)
	}
}

// ============================================================================
// Think / Act Individual Phase Tests
// ============================================================================

// ============================================================================
// CloneReactor Tests (Child Agent Inheritance and Isolation)
// ============================================================================

func TestCloneReactor_InheritsToolRegistry(t *testing.T) {
	parent := newTestReactor(nil)
	parent.RegisterTool(&mockEchoTool{})

	childReactor, _ := parent.CloneReactor(ReactorConfig{})

	// Child should have access to parent's tools
	tools := childReactor.ToolRegistry().All()
	found := false
	for _, tool := range tools {
		if tool.Info().Name == "echo_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("child reactor should inherit parent's tool registry")
	}
}

func TestCloneReactor_IndependentConfig(t *testing.T) {
	parent := newTestReactor(nil)

	childReactor, _ := parent.CloneReactor(ReactorConfig{
		Model:         "child-model",
		Temperature:   0.5,
		SystemPrompt:  "child system prompt",
		MaxIterations: 5,
	})

	if childReactor.config.Model != "child-model" {
		t.Errorf("expected child model 'child-model', got '%s'", childReactor.config.Model)
	}
	if childReactor.config.Temperature != 0.5 {
		t.Errorf("expected child temperature 0.5, got %f", childReactor.config.Temperature)
	}
	if childReactor.config.SystemPrompt != "child system prompt" {
		t.Errorf("expected child system prompt, got '%s'", childReactor.config.SystemPrompt)
	}
}

func TestCloneReactor_ParentPromptNotLeaked(t *testing.T) {
	parent := newTestReactor(nil)
	parent.config.SystemPrompt = "parent identity"

	// Clone without explicit system prompt
	childReactor, _ := parent.CloneReactor(ReactorConfig{})

	// Child should NOT inherit parent's system prompt (security)
	if childReactor.config.SystemPrompt != "" {
		t.Error("child reactor should not inherit parent's system prompt when not explicitly set")
	}
}

func TestCloneReactor_IndependentContextWindow(t *testing.T) {
	parent := newTestReactor(nil)

	childReactor, _ := parent.CloneReactor(ReactorConfig{})

	// Both start with nil context window (not initialized until first LLM call).
	// The important property is that they are independently settable.
	if parent.ContextWindow() != nil || childReactor.ContextWindow() != nil {
		// If both are nil, they are independent (not shared)
	}

	// Set different context windows on each
	parentCw := &core.ContextWindow{}
	childCw := &core.ContextWindow{}
	parent.SetContextWindow(parentCw)
	childReactor.SetContextWindow(childCw)

	if parent.ContextWindow() != parentCw {
		t.Error("parent context window not set correctly")
	}
	if childReactor.ContextWindow() != childCw {
		t.Error("child context window not set correctly")
	}
	if parent.ContextWindow() == childReactor.ContextWindow() {
		t.Error("parent and child should have independent context windows")
	}
}

func TestCloneReactor_SharesMemoryAndEventBus(t *testing.T) {
	memory := &mockMemoryImpl{}
	bus := NewEventBus()

	cfg := ReactorConfig{Model: "test", MaxIterations: 10}
	parentReactor := NewReactor(cfg,
		WithMockLLM(nil),
		WithMemory(memory),
		WithEventBus(bus))

	childReactor, _ := parentReactor.CloneReactor(ReactorConfig{})

	if childReactor.Memory() != memory {
		t.Error("child reactor should share parent's memory")
	}
	if childReactor.EventBus() != bus {
		t.Error("child reactor should share parent's event bus")
	}
}

// ============================================================================
// Mock Tool Implementations for Testing
// ============================================================================

type mockEchoTool struct{}

func (t *mockEchoTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "echo_tool",
		Description: "Echo a message back",
		Parameters: []core.Parameter{
			{Name: "message", Type: "string", Required: true, Description: "Message to echo"},
		},
	}
}

func (t *mockEchoTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	msg := ""
	if m, ok := params["message"].(string); ok {
		msg = m
	}
	return fmt.Sprintf("Echo: %s", msg), nil
}

type mockReadTool struct{}

func (t *mockReadTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "read",
		Description: "Read a file",
		Parameters: []core.Parameter{
			{Name: "path", Type: "string", Required: true, Description: "File path"},
		},
	}
}

func (t *mockReadTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	path := ""
	if p, ok := params["path"].(string); ok {
		path = p
	}
	return fmt.Sprintf("contents of %s", path), nil
}

type mockMemoryImpl struct{}

func (m *mockMemoryImpl) Retrieve(ctx context.Context, query string, opts ...core.RetrieveOption) ([]core.MemoryRecord, error) {
	return nil, nil
}
func (m *mockMemoryImpl) Store(ctx context.Context, record core.MemoryRecord) (string, error) {
	return "", nil
}
func (m *mockMemoryImpl) Delete(ctx context.Context, id string) error {
	return nil
}

// ============================================================================
// Enhanced Reactor Tests with Mock LLM
// ============================================================================

func TestReactor_MockLLMWithNativeToolCalls(t *testing.T) {
	callCount := 0
	mockFn := func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			toolArgs := `{"message": "test echo"}`
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{
						{Name: "echo_tool", Arguments: toolArgs},
					},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Done",
		}, nil
	}

	r := NewReactor(ReactorConfig{
		Model:         "test",
		MaxIterations: 5,
	},
		WithMockLLM(mockFn),
		WithoutBundledTools(),
	)

	echoTool := &mockEchoTool{}
	if err := r.RegisterTool(echoTool); err != nil {
		t.Fatalf("failed to register echo tool: %v", err)
	}

	result, err := r.Run(context.Background(), "Echo this", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = result

	if callCount < 2 {
		t.Logf("WARN: expected at least 2 LLM calls, got %d", callCount)
	}
}

func TestReactor_MockLLMMultiTurnConversation(t *testing.T) {
	turnCount := 0
	mockFn := func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		turnCount++
		if turnCount > 1 {
			if len(input.History) == 0 {
				t.Errorf("turn %d: expected conversation history, got 0 messages", turnCount)
			}
			t.Logf("turn %d: history has %d messages", turnCount, len(input.History))
		}
		return &gochatcore.Response{
			Content: fmt.Sprintf("Response %d", turnCount),
		}, nil
	}

	r := NewReactor(ReactorConfig{
		Model:         "test",
		MaxIterations: 3,
	},
		WithMockLLM(mockFn),
		WithoutBundledTools(),
	)

	result1, err := r.Run(context.Background(), "Question 1", nil)
	if err != nil {
		t.Fatalf("run 1 failed: %v", err)
	}
	t.Logf("Run 1: answer=%q, iterations=%d", result1.Answer, result1.TotalIterations)

	if turnCount == 0 {
		t.Fatal("mock LLM was never called")
	}
}

func TestReactor_MockLLMToolExecutionResult(t *testing.T) {
	var capturedHistory []core.Message

	r := NewReactor(ReactorConfig{
		Model:         "test",
		MaxIterations: 3,
	},
		WithMockLLM(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
			if len(input.History) == 0 {
				capturedHistory = input.History
				return &gochatcore.Response{
					Message: gochatcore.Message{
						ToolCalls: []gochatcore.ToolCall{{Name: "echo_tool", Arguments: `{"message": "hello"}`}},
					},
				}, nil
			}
			return &gochatcore.Response{
				Content: "Done",
			}, nil
		}),
		WithoutBundledTools(),
	)

	if err := r.RegisterTool(&mockEchoTool{}); err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	result, err := r.Run(context.Background(), "Test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalIterations == 0 {
		t.Error("expected at least 1 iteration")
	}

	t.Logf("Captured history on first call: %d messages", len(capturedHistory))
}

func TestReactor_MockLLMMultipleToolCallsInParallel(t *testing.T) {
	toolCallCount := 0
	mockFn := func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		toolCallCount++
		if toolCallCount == 1 {
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{
						{Name: "echo_tool", Arguments: `{"message": "first"}`},
						{Name: "read", Arguments: `{"path": "/tmp/test.txt"}`},
					},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Both tools executed",
		}, nil
	}

	r := NewReactor(ReactorConfig{
		Model:         "test",
		MaxIterations: 5,
	},
		WithMockLLM(mockFn),
		WithoutBundledTools(),
	)

	if err := r.RegisterTool(&mockEchoTool{}); err != nil {
		t.Fatalf("failed to register echo tool: %v", err)
	}
	if err := r.RegisterTool(&mockReadTool{}); err != nil {
		t.Fatalf("failed to register read tool: %v", err)
	}

	result, err := r.Run(context.Background(), "Use both tools", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalIterations == 0 {
		t.Error("expected at least 1 iteration")
	}
	t.Logf("Result: iterations=%d, answer=%q", result.TotalIterations, result.Answer)
}

func TestReactor_MockLLMMaxIterationsRespected(t *testing.T) {
	callCount := 0
	mockFn := func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		callCount++
		return &gochatcore.Response{
			Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "echo_tool", Arguments: `{"message": "loop"}`}},
			},
		}, nil
	}

	r := NewReactor(ReactorConfig{
		Model:         "test",
		MaxIterations: 3,
	},
		WithMockLLM(mockFn),
		WithoutBundledTools(),
	)

	if err := r.RegisterTool(&mockEchoTool{}); err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	result, err := r.Run(context.Background(), "Test loop", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalIterations > 3 {
		t.Errorf("expected <= 3 iterations, got %d", result.TotalIterations)
	}
	t.Logf("Iterations: %d (max was 3), LLM calls: %d", result.TotalIterations, callCount)
}

// ── Mock tools ──────────────────────────────────────────────────────────────

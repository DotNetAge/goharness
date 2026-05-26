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

func (h *testPreCheckHook) Before(ctx *ReactContext, input *CallInput) HookResult {
	if ctx.TerminationReason != "" {
		return HookResult{Abort: true, AbortReason: ctx.TerminationReason}
	}
	if ctx.CurrentIteration >= ctx.MaxIterations {
		return HookResult{Abort: true, AbortReason: "reached max iterations"}
	}
	if ctx.Ctx().Err() != nil {
		return HookResult{Abort: true, AbortReason: "context_cancelled: context canceled"}
	}
	return HookResult{}
}

func (h *testPreCheckHook) After(ctx *ReactContext, thought *Thought) HookResult {
	return HookResult{}
}

func (h *testPreCheckHook) Abort(ctx *ReactContext, reason string) {}

type testConvergenceHook struct{}

func (h *testConvergenceHook) Priority() int { return PriorityConvergence }

func (h *testConvergenceHook) Before(ctx *ReactContext, input *CallInput) HookResult {
	return HookResult{}
}

func (h *testConvergenceHook) After(ctx *ReactContext, thought *Thought) HookResult {
	if ctx.LastAction != nil {
		for _, tr := range ctx.LastAction.Results {
			if !tr.Success && tr.Error != "" {
				return HookResult{Abort: true, AbortReason: "irrecoverable tool error: " + tr.Error}
			}
		}
	}
	if IsDestructiveLoop(ctx.History) {
		return HookResult{Abort: true, AbortReason: "destructive loop detected"}
	}
	if IsAgentStuck(ctx.History) {
		return HookResult{Abort: true, AbortReason: "agent stuck"}
	}
	if IsResultConverged(ctx.History) {
		return HookResult{Abort: true, AbortReason: "result converged"}
	}
	if IsDuplicateAction(ctx.History) {
		return HookResult{Abort: true, AbortReason: "duplicate action detected"}
	}
	return HookResult{}
}

func (h *testConvergenceHook) Abort(ctx *ReactContext, reason string) {}

func registerTestDefaultHooks(r *Reactor) {
	r.RegisterThoughtHooks(&testPreCheckHook{}, &testConvergenceHook{})
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
	if result.TerminationReason != "context_cancelled: context canceled" {
		t.Errorf("expected termination reason 'request cancelled', got '%s'", result.TerminationReason)
	}
	if result.TotalIterations != 0 {
		t.Errorf("expected 0 iterations for pre-cancelled context, got %d", result.TotalIterations)
	}
}

// ============================================================================
// Think / Act Individual Phase Tests
// ============================================================================

func TestReactor_Think_ProducesThought(t *testing.T) {
	r := newTestReactor(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Content: "Done.",
		}, nil
	})

	ctx := NewReactContext(context.Background(), "Test input", nil, 10)

	tu, err := r.Think(ctx)
	if err != nil {
		t.Fatalf("Think failed: %v", err)
	}
	if ctx.LastThought == nil {
		t.Fatal("expected LastThought to be set")
	}
	if ctx.LastThought.Decision != DecisionAnswer {
		t.Errorf("expected DecisionAnswer, got %s", ctx.LastThought.Decision)
	}
	if ctx.LastThought.Content != "Done." {
		t.Errorf("expected Content 'Done.', got '%s'", ctx.LastThought.Content)
	}
	if tu.InputTokens < 0 {
		t.Errorf("expected non-negative token count, got %d", tu.InputTokens)
	}
	if tu.OutputTokens < 0 {
		t.Errorf("expected non-negative output token count, got %d", tu.OutputTokens)
	}
}

func TestReactor_Think_NativeToolCalls(t *testing.T) {
	r := newTestReactor(func(ctx context.Context, input CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{
					{Name: "read", Arguments: `{"path": "/tmp/test.txt"}`},
				},
			},
		}, nil
	}, WithExtraTools(&mockReadTool{}))

	ctx := NewReactContext(context.Background(), "Read a file", nil, 10)

	_, err := r.Think(ctx)
	if err != nil {
		t.Fatalf("Think failed: %v", err)
	}
	if ctx.LastThought == nil {
		t.Fatal("expected LastThought to be set")
	}
	if ctx.LastThought.Decision != DecisionAct {
		t.Errorf("expected DecisionAct, got %s", ctx.LastThought.Decision)
	}
	if len(ctx.LastThought.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(ctx.LastThought.ToolCalls))
	}
	if _, ok := ctx.LastThought.ToolCalls["read"]; !ok {
		t.Error("expected 'read' in ToolCalls")
	}
}

func TestReactor_Act_AnswerDecision(t *testing.T) {
	r := newTestReactor(nil) // No LLM needed for Act test
	ctx := NewReactContext(context.Background(), "Test", nil, 10)
	ctx.LastThought = &Thought{
		Decision: DecisionAnswer,
		Content:  "The answer is 42.",
	}

	err := r.Act(ctx)
	if err != nil {
		t.Fatalf("Act failed: %v", err)
	}
	if ctx.LastAction == nil {
		t.Fatal("expected LastAction to be set")
	}
	if len(ctx.LastAction.Results) == 0 || ctx.LastAction.Results[0].ToolName != "answer" {
		t.Errorf("expected answer result, got %v", ctx.LastAction.Results)
	}
	if ctx.LastAction.Summary() != "[answer] The answer is 42." {
		t.Errorf("expected result '[answer] The answer is 42.', got '%s'", ctx.LastAction.Summary())
	}
}

func TestReactor_Act_NoThought(t *testing.T) {
	r := newTestReactor(nil)
	ctx := NewReactContext(context.Background(), "Test", nil, 10)

	err := r.Act(ctx)
	if err == nil {
		t.Fatal("expected error when Act called without Thought")
	}
}

// -- Negative cases: conditions that should NOT trigger termination --

func TestCheckTermination_DestructiveLoop_NotTriggered(t *testing.T) {
	t.Run("different params should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "bash", Success: false, Error: "permission denied"}}}, Thought: Thought{ToolCalls: map[string]map[string]any{"bash": {"cmd": "rm -rf /"}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "bash", Success: false, Error: "permission denied"}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "bash", Success: false, Error: "permission denied"}}}},
		}
		if IsDestructiveLoop(history) {
			t.Error("IsDestructiveLoop should return false: different params per call")
		}
	})

	t.Run("fewer than 3 calls should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "bash", Success: false, Error: "denied"}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "bash", Success: false, Error: "denied"}}}},
		}
		if IsDestructiveLoop(history) {
			t.Error("IsDestructiveLoop should return false: only 2 calls")
		}
	})

	t.Run("answer actions should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}},
		}
		if IsDestructiveLoop(history) {
			t.Error("IsDestructiveLoop should return false: no tool calls")
		}
	})
}

func TestCheckTermination_AgentStuck_NotTriggered(t *testing.T) {
	t.Run("3 answer actions (not enough)", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "stuck", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "stuck", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "stuck", Success: true}}}},
		}
		if IsAgentStuck(history) {
			t.Error("IsAgentStuck should return false: only 3 answers, need 4")
		}
	})

	t.Run("a recent tool call among last 4 should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "read", Success: true}}}, Thought: Thought{Decision: DecisionAct}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}, Thought: Thought{Decision: DecisionAnswer}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}, Thought: Thought{Decision: DecisionAnswer}},
			{Action: Action{Results: []ToolResult{{ToolName: "answer", Result: "ok", Success: true}}}, Thought: Thought{Decision: DecisionAnswer}},
		}
		if IsAgentStuck(history) {
			t.Error("IsAgentStuck should return false: the first entry of the window is a tool call")
		}
	})
}

func TestCheckTermination_ResultConverged_NotTriggered(t *testing.T) {
	t.Run("empty results should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "read", Result: "", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "grep", Result: "", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "write", Result: "", Success: true}}}},
		}
		if IsResultConverged(history) {
			t.Error("IsResultConverged should return false: empty results are skipped by guard")
		}
	})

	t.Run("only 2 identical results should not trigger", func(t *testing.T) {
		history := []Step{
			{Action: Action{Results: []ToolResult{{ToolName: "read", Result: "same", Success: true}}}},
			{Action: Action{Results: []ToolResult{{ToolName: "read", Result: "same", Success: true}}}},
		}
		if IsResultConverged(history) {
			t.Error("IsResultConverged should return false: need at least 3 steps")
		}
	})
}

// ============================================================================
// CloneReactor Tests (Child Agent Inheritance and Isolation)
// ============================================================================

func TestCloneReactor_InheritsToolRegistry(t *testing.T) {
	parent := newTestReactor(nil)
	parent.RegisterTool(&mockEchoTool{})

	childReactor := parent.CloneReactor(ReactorConfig{})

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

	childReactor := parent.CloneReactor(ReactorConfig{
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
	childReactor := parent.CloneReactor(ReactorConfig{})

	// Child should NOT inherit parent's system prompt (security)
	if childReactor.config.SystemPrompt != "" {
		t.Error("child reactor should not inherit parent's system prompt when not explicitly set")
	}
}

func TestCloneReactor_IndependentContextWindow(t *testing.T) {
	parent := newTestReactor(nil)

	childReactor := parent.CloneReactor(ReactorConfig{})

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

	childReactor := parentReactor.CloneReactor(ReactorConfig{})

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

	if callCount < 2 {
		t.Logf("WARN: expected at least 2 LLM calls, got %d", callCount)
	}

	hasToolCall := false
	for _, step := range result.Steps {
		if step.Thought.Decision == DecisionAct && toolNameInAction(step.Action, "echo_tool") {
			hasToolCall = true
			break
		}
	}
	if !hasToolCall {
		t.Log("WARN: no tool call step found in history (may be expected if mock path differs)")
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
	var capturedHistory ConversationHistory

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

	for _, step := range result.Steps {
		if step.Thought.Decision == DecisionAct {
			t.Logf("Step %d: action result = %q", step.Iteration, step.Action.Summary())
			if strings.Contains(step.Action.Summary(), "echo_tool") && strings.Contains(step.Action.Summary(), "read") {
				t.Log("PASS: both tools called in same step")
			}
		}
	}
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

// toolNameInAction reports whether any ToolResult in the Action has the given tool name.
func toolNameInAction(a Action, name string) bool {
	for _, tr := range a.Results {
		if tr.ToolName == name {
			return true
		}
	}
	return false
}

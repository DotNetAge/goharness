package action_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/internal/reactor/hooks/action"
	"github.com/DotNetAge/goreact/reactor"
)

type eventCollector struct {
	mu     sync.Mutex
	events []core.ReactEvent
	types  map[core.ReactEventType]bool
	ch     <-chan core.ReactEvent
	cancel func()
}

func newEventCollector(bus reactor.EventBus, types ...core.ReactEventType) *eventCollector {
	typeSet := make(map[core.ReactEventType]bool, len(types))
	for _, typ := range types {
		typeSet[typ] = true
	}
	ch, cancel := bus.SubscribeFiltered(func(e core.ReactEvent) bool {
		return typeSet[e.Type]
	})
	return &eventCollector{
		events: make([]core.ReactEvent, 0),
		types:  typeSet,
		ch:     ch,
		cancel: cancel,
	}
}

func (c *eventCollector) drain(timeout time.Duration) []core.ReactEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.ch:
			if !ok {
				return c.events
			}
			c.events = append(c.events, ev)
		case <-deadline:
			return c.events
		}
	}
}

func (c *eventCollector) close() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *eventCollector) countByType(typ core.ReactEventType) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, ev := range c.events {
		if ev.Type == typ {
			count++
		}
	}
	return count
}

func (c *eventCollector) getByType(typ core.ReactEventType) []core.ReactEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []core.ReactEvent
	for _, ev := range c.events {
		if ev.Type == typ {
			result = append(result, ev)
		}
	}
	return result
}

type mockTool struct {
	name string
}

func (t *mockTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        t.name,
		Description: fmt.Sprintf("Mock tool %s", t.name),
		Parameters: []core.Parameter{
			{Name: "input", Type: "string", Required: true, Description: "Input"},
		},
	}
}

func (t *mockTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	input, _ := params["input"].(string)
	return fmt.Sprintf("result from %s: %s", t.name, input), nil
}

type testPreCheckHook struct{}

func (h *testPreCheckHook) Priority() int { return reactor.PriorityPreCheck }
func (h *testPreCheckHook) BeforeLLM(sessionID string, iteration int, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}
func (h *testPreCheckHook) AfterLLM(sessionID string, iteration int, resp *reactor.LLMResponse, results []reactor.ToolResult) reactor.HookResult {
	return reactor.HookResult{}
}
func (h *testPreCheckHook) Abort(sessionID string, reason string) {}

type testConvergenceHook struct{}

func (h *testConvergenceHook) Priority() int { return reactor.PriorityConvergence }
func (h *testConvergenceHook) BeforeLLM(sessionID string, iteration int, input *reactor.CallInput) reactor.HookResult {
	return reactor.HookResult{}
}
func (h *testConvergenceHook) AfterLLM(sessionID string, iteration int, resp *reactor.LLMResponse, results []reactor.ToolResult) reactor.HookResult {
	if resp != nil && len(resp.ToolCalls) == 0 {
		return reactor.HookResult{Abort: true, AbortReason: "answer produced"}
	}
	return reactor.HookResult{}
}
func (h *testConvergenceHook) Abort(sessionID string, reason string) {}

func newTestReactorWithEvents(mockFn reactor.MockLLMFunc, extraTools ...core.FuncTool) (*reactor.Reactor, reactor.EventBus, *eventCollector) {
	bus := reactor.NewEventBus()

	cfg := reactor.ReactorConfig{
		Model:         "test-model",
		MaxIterations: 10,
	}

	opts := []reactor.ReactorOption{
		reactor.WithMockLLM(mockFn),
		reactor.WithEventBus(bus),
		reactor.WithoutBundledTools(),
	}

	r := reactor.NewReactor(cfg, opts...)

	r.RegisterLoopHooks(&testPreCheckHook{}, &testConvergenceHook{})
	r.RegisterToolHooks(action.Defaults(nil, nil, nil)...)

	for _, tool := range extraTools {
		if err := r.RegisterTool(tool); err != nil {
			panic(fmt.Sprintf("failed to register tool: %v", err))
		}
	}

	collector := newEventCollector(bus,
		core.ToolExecStart, core.ToolExecEnd,
			)

	return r, bus, collector
}

// newTestReactorWithEventsMinimal creates a Reactor with ONLY ToolEventHook (no other action hooks).
// Used to isolate which hook is causing panics.
func newTestReactorWithEventsMinimal(mockFn reactor.MockLLMFunc, extraTools ...core.FuncTool) (*reactor.Reactor, reactor.EventBus, *eventCollector) {
	bus := reactor.NewEventBus()

	cfg := reactor.ReactorConfig{
		Model:         "test-model",
		MaxIterations: 10,
	}

	opts := []reactor.ReactorOption{
		reactor.WithMockLLM(mockFn),
		reactor.WithEventBus(bus),
		reactor.WithoutBundledTools(),
	}

	r := reactor.NewReactor(cfg, opts...)

	r.RegisterLoopHooks(&testPreCheckHook{}, &testConvergenceHook{})
	r.RegisterToolHooks(&action.ToolEventHook{})

	for _, tool := range extraTools {
		if err := r.RegisterTool(tool); err != nil {
			panic(fmt.Sprintf("failed to register tool: %v", err))
		}
	}

	collector := newEventCollector(bus,
		core.ToolExecStart, core.ToolExecEnd,
	)

	return r, bus, collector
}

// ============================================================================
// Branch 0: ISOLATION TEST - Only ToolEventHook, no other action hooks
// This test isolates whether ToolEventHook itself causes the panic
// ============================================================================

func TestBranch_Isolation_OnlyToolEventHook(t *testing.T) {
	callCount := 0
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "search", Arguments: `{"input": "query"}`}},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Done",
		}, nil
	}, &mockTool{name: "search"})

	_, err := r.Run(context.Background(), "Isolation test", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	tsCount := collector.countByType(core.ToolExecStart)

	if tsCount != 1 {
		t.Errorf("expected 1 ToolExecStart, got %d", tsCount)
	}
}

// ============================================================================
// Branch 1: Run → PreCheck → Cancelled context
// Expected: 0 events (abort before any phase)
// ============================================================================

func TestBranch_Run_PreCheckCancelled(t *testing.T) {
	mockFn := func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Content: "should not reach",
		}, nil
	}

	r, _, collector := newTestReactorWithEventsMinimal(mockFn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = r.Run(ctx, "Test", nil)

	events := collector.drain(1 * time.Second)
	collector.close()

	if len(events) != 0 {
		t.Errorf("expected 0 events for cancelled context, got %d", len(events))
	}
}

// ============================================================================
// Branch 2: Run → Think → Answer (JSON decision=answer)
// Expected: 0 ToolExecStart/End (no tools)
// ============================================================================

func TestBranch_Run_Think_Answer(t *testing.T) {
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Content: "Direct answer",
		}, nil
	})

	_, err := r.Run(context.Background(), "Hello", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	events := collector.drain(1 * time.Second)
	collector.close()

	if collector.countByType(core.ToolExecStart) != 0 {
		t.Errorf("expected 0 ToolExecStart for answer decision, got %d", collector.countByType(core.ToolExecStart))
	}
	t.Logf("Branch 2-Answer: total events=%d (expect 0 tool events)", len(events))
}

// ============================================================================
// Branch 3: Run → Think → Act (single tool) → Observe → Answer
// Expected: 1 ToolExecStart + 1 ToolExecEnd
// ============================================================================

func TestBranch_Run_Think_ActSingleTool_Observe_Answer(t *testing.T) {
	callCount := 0
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{
						{Name: "search", Arguments: `{"input": "query"}`},
					},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Done",
		}, nil
	}, &mockTool{name: "search"})

	_, err := r.Run(context.Background(), "Search something", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)

	if tsCount != 1 {
		t.Errorf("expected 1 ToolExecStart, got %d", tsCount)
	}
	if teCount != 1 {
		t.Errorf("expected 1 ToolExecEnd, got %d", teCount)
	}

}

// ============================================================================
// Branch 4: Run → Think → Act (parallel tools) → Observe → Answer
// Expected: 2 ToolExecStart + 2 ToolExecEnd
// ============================================================================

func TestBranch_Run_Think_ActParallelTools_Observe_Answer(t *testing.T) {
	callCount := 0
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{
						{Name: "grep", Arguments: `{"input": "pattern"}`},
						{Name: "read", Arguments: `{"input": "file"}`},
					},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Done",
		}, nil
	}, &mockTool{name: "grep"}, &mockTool{name: "read"})

	_, err := r.Run(context.Background(), "Use parallel tools", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)

	if tsCount != 2 {
		t.Errorf("expected 2 ToolExecStart, got %d", tsCount)
	}
	if teCount != 2 {
		t.Errorf("expected 2 ToolExecEnd, got %d", teCount)
	}
}

// ============================================================================
// Branch 5: CRITICAL - Multi-iteration with tools each iteration
// This is THE bug scenario: iter1(tool) → iter2(tool) → iter3(tool) → answer
// Expected: 3 ToolExecStart + 3 ToolExecEnd
// 
// ============================================================================

func TestBranch_Run_MultiIteration_EachHasTools(t *testing.T) {
	callCount := 0
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		switch callCount {
		case 1:
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "web_search", Arguments: `{"input": "q1"}`}},
				},
			}, nil
		case 2:
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "web_search", Arguments: `{"input": "q2"}`}},
				},
			}, nil
		case 3:
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "web_fetch", Arguments: `{"input": "url"}`}},
				},
			}, nil
		default:
			return &gochatcore.Response{
				Content: "Final answer",
			}, nil
		}
	}, &mockTool{name: "web_search"}, &mockTool{name: "web_fetch"})

	result, err := r.Run(context.Background(), "Multi-iter search", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(3 * time.Second)
	collector.close()

	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)

	t.Logf("=== BRANCH 5: Multi-Iteration (THE BUG SCENARIO) ===")
	t.Logf("Total iterations: %d", result.TotalIterations)
	t.Logf("ToolExecStart=%d (EXPECT 3)", tsCount)
	t.Logf("ToolExecEnd=%d (EXPECT 3)", teCount)

	if tsCount != 3 {
		t.Errorf("expected 3 ToolExecStart, got %d", tsCount)
	}
	if teCount != 3 {
		t.Errorf("expected 3 ToolExecEnd, got %d", teCount)
	}

}

// ============================================================================
// Branch 6: Mixed iterations - iter1(tool) → iter2(answer)
// Expected: 1 ToolExecStart + 1 ToolExecEnd
// ============================================================================

func TestBranch_Run_Mixed_Iter1Tool_Iter2Answer(t *testing.T) {
	callCount := 0
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		if callCount == 1 {
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "lookup", Arguments: `{"input": "key"}`}},
				},
			}, nil
		}
		return &gochatcore.Response{
			Content: "Found it",
		}, nil
	}, &mockTool{name: "lookup"})

	_, err := r.Run(context.Background(), "Mixed test", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	tsCount := collector.countByType(core.ToolExecStart)

	if tsCount != 1 {
		t.Errorf("expected 1 ToolExecStart, got %d", tsCount)
	}
}

// ============================================================================
// Branch 7: Verify ToolExecStart contains correct details
// ============================================================================

func TestBranch_ToolExecStart_Details(t *testing.T) {
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: `{"cmd": "echo hello"}`}},
			},
		}, nil
	}, &mockTool{name: "bash"})

	_, err := r.Run(context.Background(), "Detail test", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	starts := collector.getByType(core.ToolExecStart)
	if len(starts) == 0 {
		t.Fatal("expected at least 1 ToolExecStart")
	}

	data := starts[0].Data.(core.ToolExecStartData)
	if data.ToolName != "bash" {
		t.Errorf("expected ToolName='bash', got '%s'", data.ToolName)
	}
	if data.Params == nil {
		t.Error("expected Params to be non-nil")
	}
	t.Logf("ToolExecStart: ToolName=%s, Params=%v", data.ToolName, data.Params)
}

// ============================================================================
// Branch 8: Verify ToolExecEnd contains result and duration
// ============================================================================

func TestBranch_ToolExecEnd_Details(t *testing.T) {
	r, _, collector := newTestReactorWithEventsMinimal(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "echo", Arguments: `{"input": "test"}`}},
			},
		}, nil
	}, &mockTool{name: "echo"})

	_, err := r.Run(context.Background(), "End detail test", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	ends := collector.getByType(core.ToolExecEnd)
	if len(ends) == 0 {
		t.Fatal("expected at least 1 ToolExecEnd")
	}

	data := ends[0].Data.(core.ToolExecEndData)
	if data.ToolName != "echo" {
		t.Errorf("expected ToolName='echo', got '%s'", data.ToolName)
	}
	if data.Error != "" {
		t.Errorf("expected no error, got '%s'", data.Error)
	}
	if data.Duration <= 0 {
		t.Errorf("expected positive Duration, got %d", data.Duration)
	}
	if data.Result == "" {
		t.Error("expected non-empty Result")
	}
	t.Logf("ToolExecEnd: ToolName=%s, Result=%q, Duration=%dms",
		data.ToolName, truncate(data.Result, 50), data.Duration)
}

// ============================================================================
// Branch 9: MaxIterations reached during loop
// Expected: events up to max iterations, then abort
// ============================================================================

func TestBranch_Run_MaxIterationsReached(t *testing.T) {
	callCount := 0

	cfg := reactor.ReactorConfig{Model: "test", MaxIterations: 3}
	r := reactor.NewReactor(cfg,
		reactor.WithMockLLM(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
			callCount++
			return &gochatcore.Response{
				Message: gochatcore.Message{
					ToolCalls: []gochatcore.ToolCall{{Name: "loop_tool", Arguments: `{"input": "loop"}`}},
				},
			}, nil
		}),
		reactor.WithoutBundledTools(),
	)
	r.RegisterLoopHooks(&testPreCheckHook{}, &testConvergenceHook{})
	r.RegisterToolHooks(action.Defaults(nil, nil, nil)...)
	_ = r.RegisterTool(&mockTool{name: "loop_tool"})

	bus := reactor.NewEventBus()
	collector := newEventCollector(bus,
		core.ToolExecStart, core.ToolExecEnd,
	)

	result, err := r.Run(context.Background(), "Loop test", nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	_ = collector.drain(2 * time.Second)
	collector.close()

	if result.TotalIterations > 3 {
		t.Errorf("expected ≤3 iterations, got %d", result.TotalIterations)
	}
}

// ============================================================================
// Helpers
// ============================================================================

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

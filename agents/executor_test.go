package agents

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// newTestRuntimeWithTools 创建一个只包含指定工具的测试 Runtime。
// 它会先移除 NewRuntime 注册的所有默认工具，再注册测试工具，避免默认工具干扰断言。
func newTestRuntimeWithTools(t testingT, toolList []tools.FuncTool, opts ...RuntimeConfig) *Runtime {
	t.Helper()
	rt := newTestRuntime(t, opts...)
	// NewRuntime 会注册默认工具，测试前清空，仅保留测试工具。
	if reg, ok := rt.toolReg.(*tools.DefaultToolRegistry); ok {
		for _, tt := range reg.All() {
			_ = reg.Remove(tt.Info().Name)
		}
	}
	for _, tt := range toolList {
		_ = rt.toolReg.Register(tt)
	}
	return rt
}

// responseStream 构造一个只返回文本内容的 LLM 响应流。
func responseStream(content, finishReason string) *gochatcore.Stream {
	return mockStream([]gochatcore.StreamEvent{
		{Type: gochatcore.EventContent, Content: content},
		{Type: gochatcore.EventDone, FinishReason: finishReason},
	})
}

// toolCallStream 构造一个调用指定工具的 LLM 响应流。
func toolCallStream(id, name, args, finishReason string) *gochatcore.Stream {
	return mockStream([]gochatcore.StreamEvent{
		{
			Type: gochatcore.EventToolCall,
			ToolCallDeltas: []gochatcore.ToolCallDelta{
				{Index: 0, ID: id, Name: name, Arguments: args},
			},
		},
		{Type: gochatcore.EventDone, FinishReason: finishReason},
	})
}

// TestExecDirectAnswer 验证 LLM 直接给出最终答案时，循环正常结束。
func TestExecDirectAnswer(t *testing.T) {
	t.Parallel()
	calc := newFakeTool("calculator", nil)
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})
	sess := newTestSession(t)

	llm := newMockLLMClient(responseStream("最终答案是 42", "stop"))
	rt.llmClient = llm

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "1+1=？", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "completed" {
		t.Fatalf("期望终止原因 completed，得到: %s", result.TerminationReason)
	}
	if result.Answer != "最终答案是 42" {
		t.Fatalf("期望答案，得到: %s", result.Answer)
	}
	if result.Iterations != 1 {
		t.Fatalf("期望迭代 1 次，得到: %d", result.Iterations)
	}
	if !rec.has(events.FinalAnswer) {
		t.Fatal("期望收到 FinalAnswer 事件")
	}
	if !rec.has(events.ExecutionSummary) {
		t.Fatal("期望收到 ExecutionSummary 事件")
	}

	msgs := sess.Current()
	if len(msgs) != 2 {
		t.Fatalf("期望会话中有 2 条消息，得到: %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("第一条消息应为 user，得到: %s", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Fatalf("第二条消息应为 assistant，得到: %s", msgs[1].Role)
	}
}

// TestExecSingleToolCallThenAnswer 验证单次工具调用后得到答案的完整消息流。
func TestExecSingleToolCallThenAnswer(t *testing.T) {
	t.Parallel()
	calc := newFakeTool("calculator", nil)
	calc.execute = func(_ context.Context, params map[string]any) (any, error) {
		return "计算结果: 42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		responseStream("答案是 42", "stop"),
	)

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "计算 1+1", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "completed" {
		t.Fatalf("期望终止原因 completed，得到: %s", result.TerminationReason)
	}
	if calc.invokeCount != 1 {
		t.Fatalf("期望工具被调用 1 次，得到: %d", calc.invokeCount)
	}
	if !rec.has(events.ToolExecStart) || !rec.has(events.ToolExecEnd) {
		t.Fatal("期望收到 ToolExecStart/ToolExecEnd 事件")
	}

	msgs := sess.Current()
	if len(msgs) != 4 {
		t.Fatalf("期望会话中有 4 条消息，得到: %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("第 1 条消息应为 user，得到: %s", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("第 2 条消息应为带 tool_call 的 assistant")
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("第 3 条消息应为 tool 且 tool_call_id 匹配")
	}
	if msgs[3].Role != "assistant" {
		t.Fatalf("第 4 条消息应为 assistant，得到: %s", msgs[3].Role)
	}
}

// TestExecPermissionPending 验证 Grant 拒绝时循环暂停，魔法词 Allow 后恢复执行。
func TestExecPermissionPending(t *testing.T) {
	t.Parallel()
	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "计算结果: 42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"))

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "计算 1+1", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "permission_pending" {
		t.Fatalf("期望终止原因 permission_pending，得到: %s", result.TerminationReason)
	}
	if !rec.has(events.PermissionPending) {
		t.Fatal("期望收到 PermissionPending 事件")
	}

	// 模拟用户回复 PermissionAllow，工具应被执行并恢复循环。
	rt.llmClient = newMockLLMClient(responseStream("已完成", "stop"))
	result2, err := rt.Ask("test-agent", tools.PermissionAllow, sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("恢复执行后期望无错误，得到: %v", err)
	}
	if calc.invokeCount != 1 {
		t.Fatalf("期望工具被调用 1 次，得到: %d", calc.invokeCount)
	}
	if result2.TerminationReason != "completed" {
		t.Fatalf("期望终止原因 completed，得到: %s", result2.TerminationReason)
	}
}

// TestExecAskUserPending 验证 AskUser 工具调用后循环以 ask_user_pending 终止。
func TestExecAskUserPending(t *testing.T) {
	t.Parallel()
	askUser := newFakeTool("AskUser", nil)
	askUser.execute = func(_ context.Context, params map[string]any) (any, error) {
		return "已提问", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{askUser})
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(toolCallStream("call_1", "AskUser", `{"question":"你好吗？"}`, "tool_calls"))

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "问候用户", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "ask_user_pending" {
		t.Fatalf("期望终止原因 ask_user_pending，得到: %s", result.TerminationReason)
	}
	if !rec.has(events.AskUserPending) {
		t.Fatal("期望收到 AskUserPending 事件")
	}
}

// TestExecMaxIterations 验证达到最大轮次时触发 MaxTurnsReached 事件。
func TestExecMaxIterations(t *testing.T) {
	t.Parallel()
	calc := newFakeTool("calculator", nil)
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "done", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc}, WithModel(config.ModelConfig{MaxTurns: 2}))
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"a"}`, "tool_calls"),
		toolCallStream("call_2", "calculator", `{"value":"b"}`, "tool_calls"),
	)

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "一直算", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "max_iterations" {
		t.Fatalf("期望终止原因 max_iterations，得到: %s", result.TerminationReason)
	}
	if result.Iterations != 2 {
		t.Fatalf("期望迭代 2 次，得到: %d", result.Iterations)
	}
	if !rec.has(events.MaxTurnsReached) {
		t.Fatal("期望收到 MaxTurnsReached 事件")
	}
}

// TestExecContextCancelled 验证上下文取消时循环以 cancelled 终止。
func TestExecContextCancelled(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, nil)
	sess := newTestSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := rt.Ask("test-agent", "问题", sess).
		WithContext(ctx).
		Run()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到: %v", err)
	}
	if result.TerminationReason != "cancelled" {
		t.Fatalf("期望终止原因 cancelled，得到: %s", result.TerminationReason)
	}
}

// TestExecHookAbort 验证 BeforeLLM hook 可以中止循环。
func TestExecHookAbort(t *testing.T) {
	t.Parallel()
	hook := &abortHook{reason: "测试中止"}
	rt := newTestRuntimeWithTools(t, nil, WithLoopHooks(hook))
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(responseStream("不应到达", "stop"))

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "问题", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "hook_abort" {
		t.Fatalf("期望终止原因 hook_abort，得到: %s", result.TerminationReason)
	}
	if result.Answer != "测试中止" {
		t.Fatalf("期望答案为 hook 中止原因，得到: %s", result.Answer)
	}
}

// TestExecLLMStreamError 验证 LLM 流返回错误时以 llm_error 终止。
func TestExecLLMStreamError(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, nil)
	sess := newTestSession(t)

	errStream := mockStream([]gochatcore.StreamEvent{
		{Type: gochatcore.EventError, Err: errors.New("模拟 LLM 错误")},
	})
	rt.llmClient = newMockLLMClient(errStream)

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "问题", sess).
		OnEvent(rec.record).
		Run()

	if err == nil {
		t.Fatal("期望返回错误")
	}
	if result.TerminationReason != "llm_error" {
		t.Fatalf("期望终止原因 llm_error，得到: %s", result.TerminationReason)
	}
	if !rec.has(events.LLMTimeout) {
		t.Fatal("期望收到 LLMTimeout 事件")
	}
}

// TestExecBackfillToolCallID 验证缺失 tool_call_id 时会话消息仍然配对。
func TestExecBackfillToolCallID(t *testing.T) {
	t.Parallel()
	calc := newFakeTool("calculator", nil)
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})
	sess := newTestSession(t)

	stream := mockStream([]gochatcore.StreamEvent{
		{
			Type: gochatcore.EventToolCall,
			ToolCallDeltas: []gochatcore.ToolCallDelta{
				{Index: 0, Name: "calculator", Arguments: `{"value":"1+1"}`},
			},
		},
		{Type: gochatcore.EventDone, FinishReason: "tool_calls"},
	})
	rt.llmClient = newMockLLMClient(stream, responseStream("答案是 42", "stop"))

	_, err := rt.Ask("test-agent", "计算", sess).Run()
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}

	msgs := sess.Current()
	if len(msgs) != 4 {
		t.Fatalf("期望 4 条消息，得到: %d", len(msgs))
	}
	assistant := msgs[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID == "" {
		t.Fatal("期望 assistant 消息包含回填的 tool_call_id")
	}
	if msgs[2].ToolCallID != assistant.ToolCalls[0].ID {
		t.Fatalf("tool 消息的 tool_call_id 应与 assistant 配对: %s vs %s", msgs[2].ToolCallID, assistant.ToolCalls[0].ID)
	}
}

// TestExecAsyncTools 验证标记为异步的工具会并发执行。
func TestExecAsyncTools(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	started := make(map[string]time.Time)

	slow := newFakeTool("slow", nil)
	slow.isAsync = true
	slow.execute = func(_ context.Context, _ map[string]any) (any, error) {
		mu.Lock()
		started["slow"] = time.Now()
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return "slow-done", nil
	}

	fast := newFakeTool("fast", nil)
	fast.isAsync = true
	fast.execute = func(_ context.Context, _ map[string]any) (any, error) {
		mu.Lock()
		started["fast"] = time.Now()
		mu.Unlock()
		return "fast-done", nil
	}

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{slow, fast})
	sess := newTestSession(t)

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_slow", "slow", `{}`, "tool_calls"),
		toolCallStream("call_fast", "fast", `{}`, "tool_calls"),
		responseStream("完成", "stop"),
	)

	start := time.Now()
	_, err := rt.Ask("test-agent", "并发", sess).Run()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("异步工具应并发执行，耗时过长: %v", elapsed)
	}
}

// TestExecAppendFailure 验证 session.Append 失败时循环以 error 终止。
func TestExecAppendFailure(t *testing.T) {
	t.Parallel()
	store := newFakeSessionStore()
	store.appendErr = errors.New("写入失败")

	sess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	store.ensureMeta(sess)

	rt := newTestRuntimeWithTools(t, nil)
	rt.llmClient = newMockLLMClient(responseStream("hi", "stop"))

	result, err := rt.Ask("test-agent", "问题", sess).Run()
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if result.TerminationReason != "error" {
		t.Fatalf("期望终止原因 error，得到: %s", result.TerminationReason)
	}
	if !strings.Contains(err.Error(), "追加") {
		t.Fatalf("错误信息应包含追加失败，得到: %v", err)
	}
}

// TestExecTokenUsageRecorded 验证 provider 返回 usage 时会记录并发送事件。
func TestExecTokenUsageRecorded(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, nil)
	sess := newTestSession(t)

	usageStream := mockStream([]gochatcore.StreamEvent{
		{Type: gochatcore.EventContent, Content: "ok"},
		{
			Type:         gochatcore.EventDone,
			FinishReason: "stop",
			Usage: &gochatcore.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		},
	})
	rt.llmClient = newMockLLMClient(usageStream)

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "问题", sess).
		OnEvent(rec.record).
		Run()

	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TokenUsage.TotalTokens != 15 {
		t.Fatalf("期望总 token 15，得到: %d", result.TokenUsage.TotalTokens)
	}
	if !rec.has(events.TokenUsageRecorded) {
		t.Fatal("期望收到 TokenUsageRecorded 事件")
	}
}

// abortHook 是一个总是中止循环的测试 hook。
type abortHook struct {
	reason string
}

func (h *abortHook) Priority() int { return 1 }

func (h *abortHook) BeforeLLM(_ string, _ int, _ *hooks.CallInput) hooks.HookResult {
	return hooks.HookResult{Abort: true, AbortReason: h.reason}
}

func (h *abortHook) AfterLLM(_ string, _ int, _ *hooks.LLMResponse, _ []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *abortHook) Abort(_, _ string) {}

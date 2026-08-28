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

// TestExecDuplicateErrorGuide 验证同一工具连续出现完全相同错误时，
// 达到阈值后会在工具结果中注入引导话术，且后续模型给出答案后正常结束。
func TestExecDuplicateErrorGuide(t *testing.T) {
	t.Parallel()
	flaky := newFakeTool("flaky_tool", nil)
	flaky.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return nil, errors.New("参数错误: 无法解析 path")
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{flaky})
	sess := newTestSession(t)

	// 10 秒超时保护，防止循环异常导致测试挂起。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		toolCallStream("call_2", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		responseStream("我换一种方式完成任务", "stop"),
	)

	result, err := rt.Ask("test-agent", "请执行任务", sess).
		WithContext(ctx).
		Run()
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "completed" {
		t.Fatalf("期望终止原因 completed，得到: %s", result.TerminationReason)
	}
	if flaky.invokeCount != 2 {
		t.Fatalf("期望工具被调用 2 次，得到: %d", flaky.invokeCount)
	}

	msgs := sess.Current()
	// 消息顺序：user, assistant(tc), tool, assistant(tc), tool, assistant(answer)
	if len(msgs) != 6 {
		t.Fatalf("期望会话中有 6 条消息，得到: %d", len(msgs))
	}
	firstTool := msgs[2]
	secondTool := msgs[4]
	if firstTool.Role != "tool" || secondTool.Role != "tool" {
		t.Fatalf("期望第 3、5 条消息为 tool，得到: %s / %s", firstTool.Role, secondTool.Role)
	}
	if strings.Contains(firstTool.Content, "连续 2 次") {
		t.Fatal("第一次工具结果不应包含引导话术")
	}
	if !strings.Contains(secondTool.Content, "连续 2 次") {
		t.Fatalf("第二次工具结果应包含引导话术，得到: %s", secondTool.Content)
	}
	if !strings.Contains(secondTool.Content, "我这样做的目的是什么") {
		t.Fatalf("引导话术应采用第一人称自我反思格式，得到: %s", secondTool.Content)
	}
	if !strings.Contains(secondTool.Content, "是否还有其它方法") {
		t.Fatalf("引导话术应引导思考替代方案，得到: %s", secondTool.Content)
	}
}

// TestExecDuplicateErrorGuideResetsOnSuccess 验证工具成功执行会重置重复错误计数，
// 失败与成功交替出现时永不触发引导话术注入。
func TestExecDuplicateErrorGuideResetsOnSuccess(t *testing.T) {
	t.Parallel()
	flaky := newFakeTool("flaky_tool", nil)
	flaky.execute = func(_ context.Context, _ map[string]any) (any, error) {
		if flaky.invokeCount%2 == 1 {
			return nil, errors.New("参数错误: 无法解析 path")
		}
		return "成功结果", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{flaky})
	sess := newTestSession(t)

	// 10 秒超时保护，防止循环异常导致测试挂起。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		toolCallStream("call_2", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		toolCallStream("call_3", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		toolCallStream("call_4", "flaky_tool", `{"value":"x"}`, "tool_calls"),
		responseStream("任务完成", "stop"),
	)

	result, err := rt.Ask("test-agent", "请执行任务", sess).
		WithContext(ctx).
		Run()
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if result.TerminationReason != "completed" {
		t.Fatalf("期望终止原因 completed，得到: %s", result.TerminationReason)
	}
	if flaky.invokeCount != 4 {
		t.Fatalf("期望工具被调用 4 次，得到: %d", flaky.invokeCount)
	}

	for i, m := range sess.Current() {
		if m.Role == "tool" && strings.Contains(m.Content, "连续 2 次") {
			t.Fatalf("工具成功与失败交替时不应注入引导话术，第 %d 条消息: %s", i, m.Content)
		}
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

// TestExecLLMStreamError 验证 LLM 流返回普通错误时以 llm_error 终止并发送 Error 事件。
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
	if !rec.has(events.Error) {
		t.Fatal("期望收到 Error 事件")
	}
}

// TestExecLLMStreamTimeout 验证 LLM 流真实超时（DeadlineExceeded）时以 llm_timeout 终止并发送 LLMTimeout 事件。
func TestExecLLMStreamTimeout(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, nil)
	sess := newTestSession(t)

	errStream := mockStream([]gochatcore.StreamEvent{
		{Type: gochatcore.EventError, Err: context.DeadlineExceeded},
	})
	rt.llmClient = newMockLLMClient(errStream)

	rec := &eventRecorder{}
	result, err := rt.Ask("test-agent", "问题", sess).
		OnEvent(rec.record).
		Run()

	if err == nil {
		t.Fatal("期望返回错误")
	}
	if result.TerminationReason != "llm_timeout" {
		t.Fatalf("期望终止原因 llm_timeout，得到: %s", result.TerminationReason)
	}
	if !rec.has(events.LLMTimeout) {
		t.Fatal("期望收到 LLMTimeout 事件")
	}
}

// TestLLMTimeoutFromCtx 验证单次 LLM 调用超时计算：模型配置优先，其次 ctx 截止时间，
// 最后回退默认值。
func TestLLMTimeoutFromCtx(t *testing.T) {
	t.Parallel()
	fallback := defaultLLMTimeout

	t.Run("模型配置 request_timeout 优先", func(t *testing.T) {
		ctx := context.Background()
		got := llmCallTimeout(10*60, ctx) // 10 分钟
		if got != 10*time.Minute {
			t.Fatalf("期望 10 分钟，得到: %v", got)
		}
	})

	t.Run("ctx 带截止时间返回剩余时长", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		got := llmCallTimeout(0, ctx)
		if got <= 0 || got > 10*time.Minute {
			t.Fatalf("期望返回 ctx 剩余时长（约 10 分钟），得到: %v", got)
		}
		if got == fallback {
			t.Fatalf("ctx 有截止时间时不应回退默认值: %v", got)
		}
	})

	t.Run("均未配置回退默认值", func(t *testing.T) {
		ctx := context.Background()
		if got := llmCallTimeout(0, ctx); got != fallback {
			t.Fatalf("期望回退默认值 %v，得到: %v", fallback, got)
		}
	})

	t.Run("截止时间已过期回退默认值", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		if got := llmCallTimeout(0, ctx); got != fallback {
			t.Fatalf("截止时间已过期应回退默认值 %v，得到: %v", fallback, got)
		}
	})
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

// imageReadTool 构造一个返回图片数据的 fakeTool（模拟 Read 读取图片文件）。
// 返回值携带 ReadResult.Images，与真实 Read 的图片分支行为一致。
func imageReadTool() *fakeTool {
	read := newFakeTool("Read", nil)
	read.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return &tools.ReadResult{
			Data: &tools.ReadData{
				Success: true, Path: "/tmp/a.png", Content: "图片摘要：512x300",
			},
			Images: []tools.ImageContent{
				{MediaType: "image/png", Base64Data: "aGVsbG8=", Width: 512, Height: 300},
			},
		}, nil
	}
	return read
}

// TestExecReadImageAppendsImageMessage 验证图片读取的完整 Hook 链路：
// Read 返回图片 → executor 提取 Images → ImageHook 转换为图片块 →
// 以 user 角色图片消息（image_url 视觉消息）追加进上下文，
// 而非混入工具结果的文本内容。
func TestExecReadImageAppendsImageMessage(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{imageReadTool()},
		WithModel(config.ModelConfig{Name: "m", Provider: "mock", Visioning: true}))
	sess := newTestSession(t)

	// 10 秒超时保护，防止循环异常导致测试挂起。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "Read", `{"filePath":"/tmp/a.png"}`, "tool_calls"),
		responseStream("图片已分析", "stop"),
	)

	_, err := rt.Ask("test-agent", "分析图片", sess).WithContext(ctx).Run()
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}

	msgs := sess.Current()
	// 消息顺序：user, assistant(tc), tool, user(图片消息), assistant(answer)
	if len(msgs) != 5 {
		t.Fatalf("期望 5 条消息（含图片消息），得到: %d", len(msgs))
	}
	imgMsg := msgs[3]
	if imgMsg.Role != "user" {
		t.Fatalf("第 4 条消息应为 user（图片消息），得到: %s", imgMsg.Role)
	}
	if len(imgMsg.Images) != 1 {
		t.Fatalf("期望图片消息携带 1 个图片块，得到: %d", len(imgMsg.Images))
	}
	if imgMsg.Images[0].MediaType != "image/png" || imgMsg.Images[0].Base64Data != "aGVsbG8=" {
		t.Errorf("图片块内容不正确: %+v", imgMsg.Images[0])
	}
	// 工具结果文本中不应出现 base64 图片数据
	if strings.Contains(msgs[2].Content, "aGVsbG8=") {
		t.Error("base64 图片数据不应混入工具结果文本")
	}
}

// TestExecReadImageNonVisionNoImageMessage 验证非视觉模型（Visioning=false）
// 不会产生图片消息：ImageHook 未注册，图片数据不会进入上下文。
func TestExecReadImageNonVisionNoImageMessage(t *testing.T) {
	t.Parallel()
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{imageReadTool()},
		WithModel(config.ModelConfig{Name: "m", Provider: "mock"}))
	sess := newTestSession(t)

	// 10 秒超时保护，防止循环异常导致测试挂起。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "Read", `{"filePath":"/tmp/a.png"}`, "tool_calls"),
		responseStream("完成", "stop"),
	)

	_, err := rt.Ask("test-agent", "分析图片", sess).WithContext(ctx).Run()
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}

	msgs := sess.Current()
	// 消息顺序：user, assistant(tc), tool, assistant(answer) —— 无图片消息
	if len(msgs) != 4 {
		t.Fatalf("期望 4 条消息（无图片消息），得到: %d", len(msgs))
	}
	for i, m := range msgs {
		if len(m.Images) > 0 {
			t.Errorf("非视觉模型不应产生图片消息，第 %d 条: %+v", i, m.Images)
		}
	}
}

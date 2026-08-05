package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/require"
)

// spawnInBubble 在 goroutine 中运行子智能体 spawn，模拟 SubAgentTool 的异步执行方式。
// 返回结果通道供测试断言；父级 ToolContext 的 Session 提供子会话的创建参数
// （ProjectDir / AgentName / SessionStore）。
func spawnInBubble(t *testing.T, rt *Runtime, parentSess *session.Session, agentName, task string) <-chan struct {
	answer string
	sid    string
	err    error
} {
	t.Helper()
	tc := &tools.ToolContext{
		Session:      parentSess,
		SessionStore: parentSess.Store(),
		Logger:       logging.NewNopLogger(),
		EmitEvent:    func(events.ReactEvent) {},
	}
	spawnCtx := tools.WithToolContext(context.Background(), tc)

	resultCh := make(chan struct {
		answer string
		sid    string
		err    error
	}, 1)
	go func() {
		answer, sid, err := rt.subAgents.spawn(spawnCtx, agentName, task, "")
		resultCh <- struct {
			answer string
			sid    string
			err    error
		}{answer: answer, sid: sid, err: err}
	}()
	return resultCh
}

// waitSubPending 轮询等待子会话挂起等待授权。
// 子会话 exec 在 waitForPermissionDecision 中登记后才可被主会话路由授权。
func waitSubPending(t *testing.T, rt *Runtime) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, ch := rt.subAgents.findPendingSub("")
		return ch != nil
	}, 5*time.Second, 10*time.Millisecond, "子会话应挂起等待授权")
}

// bubbleResolveMagic 创建主会话 builder 并调用 resolvePermissionMagicWord，
// 模拟主会话收到用户魔法词后的授权路由。返回 (consumed, routedToSub)：
// routedToSub 为 true 表示魔法词被纯路由到子会话，主会话无需 LLM 响应。
func bubbleResolveMagic(t *testing.T, rt *Runtime, parentSess *session.Session, magicWord string) (bool, bool) {
	t.Helper()
	builder := &AskBuilder{
		ctx:      context.Background(),
		question: magicWord,
		session:  parentSess,
	}
	return rt.resolvePermissionMagicWord(
		context.Background(), builder, nil,
		func(events.ReactEventType, any) {}, rt.logger,
	)
}

// TestSubAgentPermissionBubble_Allow 验证子智能体授权冒泡的完整链路：
// 子会话遇到需要授权的工具时挂起等待 → 主会话魔法词经 resolvePermissionMagicWord
// 路由到子会话 → 子会话执行挂起工具并继续循环 → 最终给出答案。
func TestSubAgentPermissionBubble_Allow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "计算结果: 42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})

	// 共享存储：主会话与子会话使用同一 SessionStore。
	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 子会话 LLM 序列：第一轮调用 calculator（需授权），授权后第二轮直接给出答案。
	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		responseStream("子代理计算结果: 42", "stop"),
	)

	resultCh := spawnInBubble(t, rt, mainSess, "sub-agent", "请计算 1+1")

	// 等待子会话挂起等待授权。
	waitSubPending(t, rt)

	// 主会话收到用户魔法词 PermissionAllow，路由到子会话。
	consumed, routed := bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow)
	require.True(t, consumed, "魔法词应被消费并路由到子智能体")
	require.True(t, routed, "无目标魔法词路由到子会话后，主会话无需 LLM 响应")
	// 魔法词不应在主会话留下待处理授权（冒泡到子会话后主会话无 pending）。
	require.Nil(t, mainSess.TakePendingPermission(), "主会话不应残留待处理授权")

	select {
	case res := <-resultCh:
		require.NoError(t, res.err)
		require.Equal(t, "子代理计算结果: 42", res.answer)
		require.NotEmpty(t, res.sid)
		require.Equal(t, 1, calc.invokeCount, "授权后工具应执行 1 次")
		// 授权决策处理后子会话不应残留待处理授权：
		// 子会话被复用（延续上下文）后若残留旧 pending，状态不清，
		// 且新任务问题恰好是魔法词时会被误消费。
		st := rt.subAgents.states[res.sid]
		require.NotNil(t, st)
		require.False(t, st.sess.HasPendingPermission(), "子会话不应残留待处理授权")
	case <-ctx.Done():
		t.Fatal("等待子智能体完成超时")
	}
}

// TestSubAgentPermissionBubble_Deny 验证子智能体授权被拒（PermissionDeny）场景：
// 子会话收到拒绝决策后合成"权限被拒绝"工具结果并继续循环，工具本身不被执行。
func TestSubAgentPermissionBubble_Deny(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "不应执行", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 子会话 LLM 序列：第一轮调用 calculator（需授权），拒绝后第二轮调整思路给出答案。
	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		responseStream("好的，我换一种不需要授权的方式完成", "stop"),
	)

	resultCh := spawnInBubble(t, rt, mainSess, "sub-agent", "请计算 1+1")

	waitSubPending(t, rt)

	consumed, routed := bubbleResolveMagic(t, rt, mainSess, tools.PermissionDeny)
	require.True(t, consumed, "拒绝魔法词应被消费并路由到子智能体")
	require.True(t, routed, "路由到子会话后主会话无需 LLM 响应")

	select {
	case res := <-resultCh:
		require.NoError(t, res.err)
		require.Equal(t, "好的，我换一种不需要授权的方式完成", res.answer)
		require.Equal(t, 0, calc.invokeCount, "被拒绝的工具不应被执行")
		// 子会话上下文应包含拒绝引导话术（对 LLM 可见）。
		allMsgs, err := mainSess.Store().Get(context.Background(), res.sid)
		require.NoError(t, err)
		deniedFound := false
		for _, m := range allMsgs {
			if m.Role == "tool" && strings.Contains(m.Content, "用户明确表示不允许授权") {
				deniedFound = true
				break
			}
		}
		require.True(t, deniedFound, "子会话应包含权限被拒绝的引导工具结果")
	case <-ctx.Done():
		t.Fatal("等待子智能体完成超时")
	}
}

// TestSubAgentPermissionBubble_TerminationMarker 验证子会话无最终答案终止时写入终止标记：
// 拒绝授权后子会话继续，但 LLM 第二轮报错（llm_error）→ 无答案终止 →
// spawn 追加终止标记，CollectResults 的 findFinalAnswer 可据此快速判定失败。
func TestSubAgentPermissionBubble_TerminationMarker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "不应执行", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 子会话 LLM 序列：第一轮调用 calculator（需授权），第二轮返回 LLM 错误。
	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		mockStream([]gochatcore.StreamEvent{
			{Type: gochatcore.EventError, Err: errors.New("模拟 LLM 错误")},
		}),
	)

	resultCh := spawnInBubble(t, rt, mainSess, "sub-agent", "请计算 1+1")

	waitSubPending(t, rt)

	consumed, routed := bubbleResolveMagic(t, rt, mainSess, tools.PermissionDeny)
	require.True(t, consumed, "拒绝魔法词应被消费并路由到子智能体")
	require.True(t, routed, "路由到子会话后主会话无需 LLM 响应")

	select {
	case res := <-resultCh:
		require.Error(t, res.err, "LLM 报错时子智能体应返回错误")
		require.Empty(t, res.answer)
		// 终止标记应写入子会话，供 CollectResults 快速判定失败。
		allMsgs, err := mainSess.Store().Get(context.Background(), res.sid)
		require.NoError(t, err)
		require.NotEmpty(t, allMsgs, "子会话应留有消息")
		last := allMsgs[len(allMsgs)-1]
		require.True(t, strings.HasPrefix(last.Content, tools.SubAgentTerminatedPrefix),
			"最后一条消息应为终止标记，得到: %s", last.Content)
		require.Contains(t, last.Content, "llm_error", "终止标记应携带终止原因")
	case <-ctx.Done():
		t.Fatal("等待子智能体完成超时")
	}
}

// TestSubAgentPermissionBubble_MultiRound 验证同一子会话执行循环内多次触发授权：
// 每次授权后子会话继续执行，通道在整个 spawn 生命周期内保持可用，可被多次路由授权。
func TestSubAgentPermissionBubble_MultiRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "计算结果: 42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 子会话 LLM 序列：连续两轮调用 calculator（均需授权），第三轮给出答案。
	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		toolCallStream("call_2", "calculator", `{"value":"2+2"}`, "tool_calls"),
		responseStream("两次授权后完成", "stop"),
	)

	resultCh := spawnInBubble(t, rt, mainSess, "sub-agent", "连续计算两次")

	// 第一次授权。
	waitSubPending(t, rt)
	consumed, routed := bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow)
	require.True(t, consumed, "第一次授权魔法词应被消费")
	require.True(t, routed, "路由到子会话后主会话无需 LLM 响应")

	// 第二次授权（第一次授权后子会话继续执行，再次挂起）。
	waitSubPending(t, rt)
	consumed, routed = bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow)
	require.True(t, consumed, "第二次授权魔法词应被消费")
	require.True(t, routed, "路由到子会话后主会话无需 LLM 响应")

	select {
	case res := <-resultCh:
		require.NoError(t, res.err)
		require.Equal(t, "两次授权后完成", res.answer)
		require.Equal(t, 2, calc.invokeCount, "两次授权后工具应执行 2 次")
	case <-ctx.Done():
		t.Fatal("等待子智能体完成超时")
	}
}

// TestSubAgentPermissionBubble_MagicWordNoPending 验证主会话魔法词在无任何
// 子会话挂起等待授权时不消费（返回 false，按普通用户轮次处理）。
func TestSubAgentPermissionBubble_MagicWordNoPending(t *testing.T) {
	rt := newTestRuntime(t)
	mainSess := newTestSession(t)

	consumed, _ := bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow)
	require.False(t, consumed, "无挂起子会话时魔法词不应被消费")
}

// TestSubAgentPermissionBubble_TargetedResolve 验证多子会话并发挂起时带目标魔法词
// 经 resolvePermissionMagicWord 精确路由：前端在子会话 B 的授权弹窗点击允许后
// 发送 "PermissionAllow: <B的session_id>"，后端路由到 B 而非更早挂起的 A；
// A 保持挂起，随后由无目标魔法词按先到先服务路由并完成执行。
func TestSubAgentPermissionBubble_TargetedResolve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calc := newFakeTool("calculator", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	calc.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "计算结果: 42", nil
	}
	rt := newTestRuntimeWithTools(t, []tools.FuncTool{calc})

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 真实子会话 A：第一轮调用 calculator（需授权），授权后第二轮给出答案。
	rt.llmClient = newMockLLMClient(
		toolCallStream("call_1", "calculator", `{"value":"1+1"}`, "tool_calls"),
		responseStream("子代理计算结果: 42", "stop"),
	)
	resultCh := spawnInBubble(t, rt, mainSess, "sub-agent", "请计算 1+1")
	waitSubPending(t, rt)

	// 另一个假挂起子会话 B（模拟第二个并发挂起等待授权的子会话）。
	sessB, err := session.New("sub-b", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	chB := make(chan permissionSignal, 1)
	rt.subAgents.states[sessB.ID()] = &perSessionState{sess: sessB}
	rt.subAgents.registerPermissionWait(sessB.ID(), chB)

	// 主会话收到带目标魔法词（前端在 B 的授权弹窗点击允许）：
	// 精确路由到 B，即使真实子会话 A 更早挂起。
	consumed, routed := bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow+": "+sessB.ID())
	require.True(t, consumed, "带目标魔法词应被消费")
	require.True(t, routed, "带目标魔法词被纯路由到子会话，主会话无需 LLM 响应")
	select {
	case sig := <-chB:
		require.Equal(t, tools.PermissionAllow, sig.action)
	default:
		t.Fatal("目标子会话 B 应收到授权决策")
	}

	// A 仍在挂起（未被误路由）：无目标魔法词按先到先服务路由到 A，A 继续执行完成。
	consumed, routed = bubbleResolveMagic(t, rt, mainSess, tools.PermissionAllow)
	require.True(t, consumed, "无目标魔法词应路由到最早挂起的 A")
	require.True(t, routed, "路由到子会话后主会话无需 LLM 响应")
	select {
	case res := <-resultCh:
		require.NoError(t, res.err)
		require.Equal(t, "子代理计算结果: 42", res.answer)
	case <-ctx.Done():
		t.Fatal("等待子智能体完成超时")
	}
}

// TestClassifyMagicWord_TargetedFormat 验证带目标魔法词解析：
// "PermissionAllow: <session_id>" 等格式返回动作 + 目标；不带目标保持旧行为。
func TestClassifyMagicWord_TargetedFormat(t *testing.T) {
	mw := tools.ClassifyMagicWord("PermissionAllow: abc123")
	require.Equal(t, tools.PermissionAllow, mw.Action)
	require.Equal(t, "abc123", mw.SessionID)

	mw = tools.ClassifyMagicWord("PermissionAllowSession: xyz")
	require.Equal(t, tools.PermissionAllowSession, mw.Action)
	require.Equal(t, "xyz", mw.SessionID)

	mw = tools.ClassifyMagicWord("PermissionDeny: qwe")
	require.Equal(t, tools.PermissionDeny, mw.Action)
	require.Equal(t, "qwe", mw.SessionID)

	// 无目标保持旧行为。
	mw = tools.ClassifyMagicWord("PermissionAllow")
	require.Equal(t, tools.PermissionAllow, mw.Action)
	require.Empty(t, mw.SessionID)

	// 大小写不敏感。
	mw = tools.ClassifyMagicWord("permissionallow: abc")
	require.Equal(t, tools.PermissionAllow, mw.Action)
	require.Equal(t, "abc", mw.SessionID)

	// 冒号后无目标 → 不识别为带目标格式（走普通魔法词分支）。
	mw = tools.ClassifyMagicWord("PermissionAllow:")
	require.Empty(t, mw.Action)
	require.Empty(t, mw.SessionID)

	// 普通用户消息。
	mw = tools.ClassifyMagicWord("你好")
	require.Empty(t, mw.Action)
	require.Empty(t, mw.SessionID)
}

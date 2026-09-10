package agents

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubAgentManager_DefaultNewSession 验证未认领的会话不参与复用：
// 会话刚创建（尚未被 spawn 锁定使用）时，不传 session_id 仍新建独立会话
// （1 ProjectDir → N Session 分身模型），避免并行分身场景误复用同一新会话。
func TestSubAgentManager_DefaultNewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "", store, "")
	require.NoError(t, err)
	require.NotNil(t, st1)

	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "", store, "")
	require.NoError(t, err)
	require.NotNil(t, st2)

	assert.NotEqual(t, st1.sess.ID(), st2.sess.ID(),
		"未认领使用过的会话不得被复用，应新建独立会话")
}

// TestSubAgentManager_ReuseIdleSession 验证空闲会话复用：
// 同一 Agent + ProjectDir + Sponsor 的会话被 spawn 使用过一次且当前空闲时，
// 再次委派应复用同一会话，延续讨论上下文，而非新开 Session 丢失上下文。
func TestSubAgentManager_ReuseIdleSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	// 模拟首次 spawn：创建会话并被认领使用（Lock + touch）后释放认领。
	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	st1.lock.Lock()
	rt.subAgents.touchSession(st1.sess.ID())
	st1.lock.Unlock()
	rt.subAgents.releaseSpawn(st1.sess.ID())

	// 同一 Agent + ProjectDir + Sponsor 空闲：应复用同一会话。
	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	require.NotNil(t, st2)
	assert.Equal(t, st1.sess.ID(), st2.sess.ID(),
		"空闲会话应被复用（延续讨论上下文），不得新开 Session")
}

// TestSubAgentManager_ConcurrentClaimNoCollapse 回归「多 SubAgent 坍缩同一会话」缺陷：
// 多个并行派发（session_id 为空）同时进行会话定位时，任一会话同一时刻至多被一个
// spawn 认领——认领判定与 spawning 置位在 m.mu 写锁内原子完成（findIdleSession /
// recoverLatestSession / 新建路径均如此），并发调用方不得拿到同一 sessionID，
// 否则并行任务在同一会话上串行撞车、CollectResults 读串、结果逐字相同。
func TestSubAgentManager_ConcurrentClaimNoCollapse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	// 预置一个已用过的空闲会话（历史遗留场景：既有空闲候选，又有并发认领）。
	st0, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	st0.lock.Lock()
	rt.subAgents.touchSession(st0.sess.ID())
	st0.lock.Unlock()
	rt.subAgents.releaseSpawn(st0.sess.ID())

	// 8 个并发派发同时定位会话：空闲的那个至多被认领一次，其余各自新建分身，
	// 任何两个派发不得拿到同一会话。
	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
			if err != nil {
				t.Errorf("并发 getOrCreate 失败: %v", err)
				return
			}
			ids[i] = st.sess.ID()
		}(i)
	}
	wg.Wait()

	seen := make(map[string]int, n)
	for _, id := range ids {
		if id == "" {
			t.Error("并发派发应返回有效会话，不得为空")
			continue
		}
		seen[id]++
	}
	for id, count := range seen {
		assert.Equal(t, 1, count, "并发派发不得坍缩到同一会话: %s 被认领 %d 次", id, count)
	}
}

// TestSubAgentManager_ActiveSpawnNewSession 验证并行分身：
// 同一 Agent 的会话正被活跃 spawn 占用（锁被持有）时，再次委派应新建独立会话并行工作，
// 而不是复用活跃会话（复用会串行化并行任务且锁上阻塞）。
func TestSubAgentManager_ActiveSpawnNewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	// 模拟活跃 spawn：持锁且已认领使用，保持活跃直至测试结束。
	st1.lock.Lock()
	rt.subAgents.touchSession(st1.sess.ID())
	defer st1.lock.Unlock()

	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	require.NotNil(t, st2)
	assert.NotEqual(t, st1.sess.ID(), st2.sess.ID(),
		"该 Agent 有活跃 spawn 时应新建分身会话并行工作")
}

// TestSubAgentManager_DifferentSponsorNoReuse 验证发起方不同不复用：
// Sponsor 不同说明讨论主题可能无关，不得复用旧会话的上下文。
func TestSubAgentManager_DifferentSponsorNoReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	st1.lock.Lock()
	rt.subAgents.touchSession(st1.sess.ID())
	st1.lock.Unlock()

	// 另一发起方（sponsor 不同）不应复用到 main-agent 发起的会话。
	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "other-agent", store, "")
	require.NoError(t, err)
	require.NotNil(t, st2)
	assert.NotEqual(t, st1.sess.ID(), st2.sess.ID(),
		"Sponsor 不同的会话不得复用，应新建独立会话")
}

// TestSubAgentManager_RecoverLatestSession 验证跨进程恢复最近会话：
// 持久化存储中存在同 Agent + ProjectDir + Sponsor 的历史会话（如上一进程遗留）时，
// 内存无登记的调用方应恢复最近使用的会话，延续历史讨论上下文。
func TestSubAgentManager_RecoverLatestSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	// 直接通过 store.Create 构造历史会话元数据（模拟上一进程遗留）。
	info, err := store.Create(ctx, "sub-agent",
		session.WithProjectDirOption(projectDir),
		session.WithSponsorOption("main-agent"),
	)
	require.NoError(t, err)

	// 全新 manager（states 为空，模拟重启）应恢复该历史会话。
	st, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	require.NotNil(t, st)
	assert.Equal(t, info.SessionID, st.sess.ID(),
		"应恢复持久化存储中最近使用的会话以延续上下文")
	assert.Equal(t, projectDir, st.sess.ProjectDir(), "恢复的会话应保留工作目录")
}

// TestSubAgentManager_ExplicitReuse 验证显式复用：传入已登记会话的 session_id 时，
// 返回同一会话实例，从而延续对话上下文。
func TestSubAgentManager_ExplicitReuse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "", store, "")
	require.NoError(t, err)
	require.NotNil(t, st1)

	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "", store, st1.sess.ID())
	require.NoError(t, err)
	require.NotNil(t, st2)

	assert.Equal(t, st1.sess.ID(), st2.sess.ID(),
		"传 session_id 应复用已登记的同一会话实例（延续对话）")
}

// TestSubAgentSpawn_TaskBoundaryMarker 验证复用会话时写入任务开始标记：
// 同一 Agent + ProjectDir + Sponsor 的第二次委派复用同一会话（延续上下文），
// spawn 应在新任务的问题消息之前追加 user 角色任务开始标记，
// 供 CollectResults 的 findFinalAnswer 划定任务边界（避免命中历史任务结果）。
func TestSubAgentSpawn_TaskBoundaryMarker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := newTestRuntimeWithTools(t, nil)
	// 两次 spawn 均直接给出答案（无需工具）。
	rt.llmClient = newMockLLMClient(
		responseStream("第一次任务结果", "stop"),
		responseStream("第二次任务结果", "stop"),
	)

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	// 第一次 spawn：新建会话。
	res1 := <-spawnInBubble(t, rt, mainSess, "sub-agent", "第一次任务")
	require.NoError(t, res1.err)
	require.Equal(t, "第一次任务结果", res1.answer)

	// 第二次 spawn：复用同一空闲会话（延续讨论上下文）。
	res2 := <-spawnInBubble(t, rt, mainSess, "sub-agent", "第二次任务")
	require.NoError(t, res2.err)
	require.Equal(t, "第二次任务结果", res2.answer)
	require.Equal(t, res1.sid, res2.sid, "空闲会话应被复用")

	// 复用会话时应写入任务开始标记，作为 CollectResults 的任务边界。
	allMsgs, err := store.Get(ctx, res2.sid)
	require.NoError(t, err)
	markerFound := false
	for _, m := range allMsgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, tools.SubAgentTaskStartPrefix) {
			markerFound = true
			break
		}
	}
	require.True(t, markerFound, "复用会话应包含任务开始标记")
}

// blockingStream 构造一个阻塞直到 release 被关闭的 LLM 响应流（随后返回
// 指定文本），用于让 exec 循环停在流消费阶段，以便断言运行中间状态。
func blockingStream(release <-chan struct{}, content string) *gochatcore.Stream {
	ch := make(chan gochatcore.StreamEvent, 2)
	go func() {
		<-release
		ch <- gochatcore.StreamEvent{Type: gochatcore.EventContent, Content: content}
		ch <- gochatcore.StreamEvent{Type: gochatcore.EventDone, FinishReason: "stop"}
		close(ch)
	}()
	return gochatcore.NewStream(ch, nil)
}

// TestSubAgentManager_CancelSponsored 验证主会话停止时的级联强停语义：
// 1) 仅取消指定主会话登记的全部子执行 ctx，其他主会话不受波及；
// 2) 强停后登记被清理（重复强停返回 0，避免重复计数）；
// 3) 注销后的句柄不再被强停命中。
func TestSubAgentManager_CancelSponsored(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	ctxC, cancelC := context.WithCancel(context.Background())
	defer cancelC()

	rt.subAgents.registerSponsored("main-1", "sub-a", cancelA)
	rt.subAgents.registerSponsored("main-1", "sub-b", cancelB)
	rt.subAgents.registerSponsored("main-2", "sub-c", cancelC)

	assert.Equal(t, 2, rt.CancelSubAgents("main-1"), "应强停 main-1 登记的全部子代理")
	assert.Error(t, ctxA.Err(), "sub-a 的执行 ctx 应被取消")
	assert.Error(t, ctxB.Err(), "sub-b 的执行 ctx 应被取消")
	assert.NoError(t, ctxC.Err(), "其他主会话派生的子代理不得被波及")

	assert.Equal(t, 0, rt.CancelSubAgents("main-1"), "强停后登记已清理，重复强停返回 0")

	ctxD, cancelD := context.WithCancel(context.Background())
	defer cancelD()
	rt.subAgents.registerSponsored("main-3", "sub-d", cancelD)
	rt.subAgents.unregisterSponsored("main-3", "sub-d")
	assert.Equal(t, 0, rt.CancelSubAgents("main-3"), "注销后的句柄不再被强停命中")
	assert.NoError(t, ctxD.Err(), "注销后的子执行 ctx 不应被取消")
}

// TestSubAgentSpawn_SponsoredRegistrationLifecycle 验证 spawn 的强停登记生命周期：
// 1) 子执行循环运行期间，以发起方主会话 ID 为键的强停登记可见；
// 2) spawn 正常结束后登记注销，登记表无残留（避免泄漏）。
func TestSubAgentSpawn_SponsoredRegistrationLifecycle(t *testing.T) {
	rt := newTestRuntimeWithTools(t, nil)

	// 阻塞式 LLM 流：spawn 进入 exec 循环后停在流消费，便于断言运行中登记。
	release := make(chan struct{})
	rt.llmClient = newMockLLMClient(blockingStream(release, "任务结果"))

	store := newFakeSessionStore()
	mainSess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)

	resCh := spawnInBubble(t, rt, mainSess, "sub-agent", "任务")

	// 运行中登记可见：sponsored 表中存在以主会话 ID 为键的强停句柄。
	require.Eventually(t, func() bool {
		rt.subAgents.mu.RLock()
		defer rt.subAgents.mu.RUnlock()
		return len(rt.subAgents.sponsored[mainSess.ID()]) == 1
	}, 5*time.Second, 5*time.Millisecond, "子执行循环运行期间应存在强停登记")

	// 释放流让 spawn 正常结束。
	close(release)
	res := <-resCh
	require.NoError(t, res.err)
	require.Equal(t, "任务结果", res.answer)

	// 结束后登记注销，无残留。
	rt.subAgents.mu.RLock()
	remaining := len(rt.subAgents.sponsored[mainSess.ID()])
	rt.subAgents.mu.RUnlock()
	assert.Equal(t, 0, remaining, "spawn 结束后应注销强停登记")
}

// TestSubAgentManager_ExplicitReuseClaimsSpawning 回归「显式复用期间被空闲认领误并行」缺陷：
// 显式传入 session_id 复用会话时，getOrCreate 必须置位 spawning 认领标志——
// 否则并发的无 session_id 派发会经 findIdleSession（判定条件含 spawning == false）
// 在显式复用占用期间把该会话当作空闲会话误认领，把本应独立的并行任务串行化到
// 同一会话（上下文混流、并行性丢失）。
func TestSubAgentManager_ExplicitReuseClaimsSpawning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()
	projectDir := t.TempDir()

	// 预置一个已用过的空闲会话（上次 spawn 已结束、spawning 已复位）。
	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	st1.lock.Lock()
	rt.subAgents.touchSession(st1.sess.ID())
	st1.lock.Unlock()
	rt.subAgents.releaseSpawn(st1.sess.ID())
	idleID := st1.sess.ID()

	// 显式复用该会话：应置位 spawning 认领标志。
	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, idleID)
	require.NoError(t, err)
	assert.True(t, st2.spawning, "显式复用必须置位 spawning 认领标志")

	// 并发的无 session_id 派发：不得误认领显式复用中的会话，应新建分身。
	st3, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	assert.NotEqual(t, idleID, st3.sess.ID(),
		"显式复用占用中的会话不得被空闲复用路径并发认领，应新建分身")
}

// TestSubAgentSpawn_RecoverPanic 验证 spawn 的 panic 兜底：
// 执行链路发生 panic 时不得击穿到调用方（tools 层后台 goroutine）崩掉整个进程，
// 而是捕获转为错误返回，并登记失败广播——CollectResults 经 waitCompletions
// 直接感知失败原因，不至对无终止标记的子会话死等轮询。
func TestSubAgentSpawn_RecoverPanic(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	// agentExists 校验在 spawn 早期阶段执行（recover 覆盖范围内），
	// 以回调 panic 模拟执行链路任意 panic 源。
	rt.agentExists = func(string) bool { panic("应用侧配置回调异常") }

	const subSessionID = "sub-session-panic-test"
	_, _, err := rt.subAgents.spawn(context.Background(), "sub-agent", "任务", subSessionID)
	require.Error(t, err, "panic 应被捕获转为错误返回，而非击穿崩溃")
	assert.Contains(t, err.Error(), "panic")

	// CollectResults 的等待入口应能感知到本次失败（spawnErrors 登记）。
	errs := rt.subAgents.waitCompletions(context.Background(), []string{subSessionID})
	require.Contains(t, errs, subSessionID, "spawn 早期 panic 应登记失败广播供 CollectResults 感知")
	assert.Contains(t, errs[subSessionID].Error(), "panic")
}

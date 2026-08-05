package agents

import (
	"context"
	"strings"
	"testing"
	"time"

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

	// 模拟首次 spawn：创建会话并被认领使用（Lock + touch）后释放。
	st1, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	st1.lock.Lock()
	rt.subAgents.touchSession(st1.sess.ID())
	st1.lock.Unlock()

	// 同一 Agent + ProjectDir + Sponsor 空闲：应复用同一会话。
	st2, err := rt.subAgents.getOrCreate(ctx, "sub-agent", projectDir, "main-agent", store, "")
	require.NoError(t, err)
	require.NotNil(t, st2)
	assert.Equal(t, st1.sess.ID(), st2.sess.ID(),
		"空闲会话应被复用（延续讨论上下文），不得新开 Session")
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

package agents

import (
	"context"
	"testing"

	"github.com/DotNetAge/goagent/subagent"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSubmitTestMainSession 构造 Submit 测试用主会话与 ToolContext。
func newSubmitTestMainSession(t *testing.T) (*session.Session, *tools.ToolContext) {
	t.Helper()
	store := newFakeSessionStore()
	mainSess, err := session.New("main-agent", "", t.TempDir(), store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(mainSess)
	tc := &tools.ToolContext{
		Session:      mainSess,
		SessionStore: mainSess.Store(),
		Logger:       logging.NewNopLogger(),
	}
	return mainSess, tc
}

// TestSubmit_AnchorRejectsDetourSession 锚点命中且 leader 显式传不同会话：
// 绕道企图被拒绝受理（不静默覆盖显式意图，也不放行从零重跑满迭代上限）。
func TestSubmit_AnchorRejectsDetourSession(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	mainSess, tc := newSubmitTestMainSession(t)
	ctx := tools.WithToolContext(context.Background(), tc)

	// 预置失败会话（真实存在，锚点指向它）
	st, err := rt.subAgents.getOrCreate(ctx, "sub-agent", mainSess.ProjectDir(), mainSess.AgentName(), mainSess.Store(), "")
	require.NoError(t, err)
	task := "一个注定超限的大任务"
	rt.subAgents.recordFailedTaskAnchor(mainSess.ID(), task, st.sess.ID())

	receipt, err := rt.subAgents.Submit(ctx, subagent.SubAgentRequest{
		AgentName: "sub-agent", Task: task, SessionID: "fresh-other-session",
	})
	require.NoError(t, err)
	require.False(t, receipt.Accepted, "显式传不同会话的绕道重派必须被拒绝")
	assert.Contains(t, receipt.Reason, "无法绕过", "拒绝原因应说明判定无法绕过")
}

// TestSubmit_AnchorConvergesUnnamed 锚点命中且未传 session_id：重派被定向到
// 失败会话（回执 SessionID 即失败会话），续跑/熔断判定在其 streak 历史上生效。
// 后台 runTask 无 LLM 客户端会失败结算并清零 streak，不影响回执收敛断言。
func TestSubmit_AnchorConvergesUnnamed(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	mainSess, tc := newSubmitTestMainSession(t)
	ctx := tools.WithToolContext(context.Background(), tc)

	st, err := rt.subAgents.getOrCreate(ctx, "sub-agent", mainSess.ProjectDir(), mainSess.AgentName(), mainSess.Store(), "")
	require.NoError(t, err)
	task := "一个注定超限的大任务"
	rt.subAgents.recordFailedTaskAnchor(mainSess.ID(), task, st.sess.ID())

	receipt, err := rt.subAgents.Submit(ctx, subagent.SubAgentRequest{
		AgentName: "sub-agent", Task: task,
	})
	require.NoError(t, err)
	require.True(t, receipt.Accepted)
	assert.Equal(t, st.sess.ID(), receipt.SessionID,
		"同任务重派未传会话时应定向到失败会话而非新建/复用其它会话")
}

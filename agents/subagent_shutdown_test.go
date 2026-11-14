package agents

import (
	"context"
	"testing"
	"time"

	"github.com/DotNetAge/goagent/subagent"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/require"
)

// V-21：排队等并发槽的任务必须能被停机中断。
//
// 修复前：runTask 里是裸的 `subagentSem <- struct{}{}`，而该 goroutine 的 ctx
// 由 context.WithoutCancel 派生（Done() 恒为 nil），所以停机时排队的 goroutine
// 会一直等到拿到槽位，拖住优雅退出。
//
// 修复后：抢槽改为 select，监听 manager 的 shutdown 信号；触发时结算失败
// 并释放 spawn 认领，避免会话永久卡在 spawning。
func TestRunTask_QueuedTaskExitsOnShutdown(t *testing.T) {
	// 占满全局信号量，强制后续任务进入排队
	slots := cap(subagentSem)
	for i := 0; i < slots; i++ {
		subagentSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < slots; i++ {
			<-subagentSem
		}
	}()

	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	_, tc := newSubmitTestMainSession(t)
	ctx := tools.WithToolContext(context.Background(), tc)

	receipt, err := rt.subAgents.Submit(ctx, subagent.SubAgentRequest{
		AgentName: "sub-agent",
		Task:      "一个在槽位队列里等待的任务",
	})
	require.NoError(t, err)
	require.True(t, receipt.Accepted, "任务应已受理")
	sid := receipt.SessionID

	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if cond() {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("超时：%s", desc)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// 前置确认：后台 goroutine 已启动并持有认领标志（说明它卡在抢槽）
	waitFor("后台任务应进入 spawning 认领态", func() bool {
		rt.subAgents.mu.RLock()
		defer rt.subAgents.mu.RUnlock()
		st := rt.subAgents.states[sid]
		return st != nil && st.spawning
	})

	// 触发停机信号
	rt.CancelAllSubAgents()

	// 关键断言：排队任务必须退出并释放认领
	waitFor("停机后排队任务应退出并释放 spawning 认领", func() bool {
		rt.subAgents.mu.RLock()
		defer rt.subAgents.mu.RUnlock()
		st := rt.subAgents.states[sid]
		return st == nil || !st.spawning
	})
}

// shutdown 只应关闭一次：重复停机不能 panic（close of closed channel）。
func TestCancelAllSubAgents_Idempotent(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))

	require.NotPanics(t, func() {
		rt.CancelAllSubAgents()
		rt.CancelAllSubAgents()
	})
}

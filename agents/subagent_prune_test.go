package agents

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

func newPruneTestManager() *subAgentManager {
	return newSubAgentManager(&Runtime{logger: logging.NewNopLogger()})
}

// V-20：states 此前既无上限也无 delete 路径，长跑进程随派发次数线性增长。
// 超限时必须按 LRU 把最旧的空闲条目清回上限。
func TestPruneStates_EvictsOldestWhenOverLimit(t *testing.T) {
	m := newPruneTestManager()
	base := time.Now()

	m.mu.Lock()
	for i := 0; i < 300; i++ {
		m.states[fmt.Sprintf("s-%03d", i)] = &perSessionState{
			lastUsedAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	m.pruneStatesLocked()
	m.mu.Unlock()

	if got := len(m.states); got != subagentStateLimit {
		t.Fatalf("期望修剪到 %d 条，实际 %d 条", subagentStateLimit, got)
	}
	// 最旧的 44 条应被驱逐
	if _, ok := m.states["s-000"]; ok {
		t.Error("最旧条目 s-000 应被驱逐")
	}
	if _, ok := m.states["s-299"]; !ok {
		t.Error("最新条目 s-299 不应被驱逐")
	}
}

// 正在运行的子会话（登记在 sponsored 下）绝不能被驱逐——删掉会丢子执行上下文。
func TestPruneStates_KeepsSponsoredActive(t *testing.T) {
	m := newPruneTestManager()
	base := time.Now()

	m.mu.Lock()
	for i := 0; i < 300; i++ {
		sid := fmt.Sprintf("s-%03d", i)
		m.states[sid] = &perSessionState{lastUsedAt: base.Add(time.Duration(i) * time.Second)}
	}
	// 把最旧的那条标成正在运行
	m.sponsored["main"] = map[string]context.CancelFunc{
		"s-000": func() {},
	}
	m.pruneStatesLocked()
	m.mu.Unlock()

	if _, ok := m.states["s-000"]; !ok {
		t.Error("运行中的子会话 s-000 不应被驱逐")
	}
}

// 挂起等待授权/提问的条目不能删——通道一断，主会话的授权决策就送不回去了。
func TestPruneStates_KeepsPendingWait(t *testing.T) {
	m := newPruneTestManager()
	base := time.Now()

	m.mu.Lock()
	for i := 0; i < 300; i++ {
		sid := fmt.Sprintf("s-%03d", i)
		m.states[sid] = &perSessionState{lastUsedAt: base.Add(time.Duration(i) * time.Second)}
	}
	m.states["s-000"].pendingCh = make(chan permissionSignal, 1)
	m.states["s-001"].pendingAskCh = make(chan string, 1)
	m.pruneStatesLocked()
	m.mu.Unlock()

	if _, ok := m.states["s-000"]; !ok {
		t.Error("等待授权的 s-000 不应被驱逐")
	}
	if _, ok := m.states["s-001"]; !ok {
		t.Error("等待提问回答的 s-001 不应被驱逐")
	}
}

// lastUsedAt 为零表示「刚创建/恢复、尚未被 spawn 认领」，按设计不参与复用，
// 也不参与驱逐——宁可留着也不误删正在准备中的会话。
func TestPruneStates_KeepsNeverUsed(t *testing.T) {
	m := newPruneTestManager()

	m.mu.Lock()
	for i := 0; i < 300; i++ {
		m.states[fmt.Sprintf("s-%03d", i)] = &perSessionState{}
	}
	m.pruneStatesLocked()
	got := len(m.states)
	m.mu.Unlock()

	if got != 300 {
		t.Errorf("全部为未使用条目时不应驱逐，期望 300 条，实际 %d 条", got)
	}
}

// 未超上限时不做任何删除。
func TestPruneStates_NoopUnderLimit(t *testing.T) {
	m := newPruneTestManager()
	base := time.Now()

	m.mu.Lock()
	for i := 0; i < subagentStateLimit; i++ {
		m.states[fmt.Sprintf("s-%03d", i)] = &perSessionState{lastUsedAt: base}
	}
	m.pruneStatesLocked()
	got := len(m.states)
	m.mu.Unlock()

	if got != subagentStateLimit {
		t.Errorf("未超限时不应驱逐，期望 %d 条，实际 %d 条", subagentStateLimit, got)
	}
}

// taskInfos 同样需要上限：按登记时间驱逐最旧的。
func TestPruneTaskInfos_EvictsOldest(t *testing.T) {
	m := newPruneTestManager()
	base := time.Now()

	m.mu.Lock()
	for i := 0; i < 300; i++ {
		m.taskInfos[fmt.Sprintf("t-%03d", i)] = harvestTaskInfo{
			sessionID:    fmt.Sprintf("s-%03d", i),
			agentName:    "a",
			title:        "x",
			registeredAt: base.Add(time.Duration(i) * time.Second),
		}
	}
	m.pruneTaskInfosLocked()
	got := len(m.taskInfos)
	_, oldest := m.taskInfos["t-000"]
	_, newest := m.taskInfos["t-299"]
	m.mu.Unlock()

	if got != subagentTaskInfoLimit {
		t.Fatalf("期望修剪到 %d 条，实际 %d 条", subagentTaskInfoLimit, got)
	}
	if oldest {
		t.Error("最旧登记 t-000 应被驱逐")
	}
	if !newest {
		t.Error("最新登记 t-299 不应被驱逐")
	}
}

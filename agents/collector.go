package agents

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/DotNetAge/goagent"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// 后台收割器（非阻塞 CollectResults 的执行体）。
//
// 旧设计：CollectResults 工具内 select rt.Done() 死等，主会话串行队列被
// 永久占住——子任务挂死即大梁无法对话（bega-runtime Bash 挂死实证）。
// 新设计：工具壳调用 StartHarvest 启动本收割器后立即返回；收割器周期
// 轮询子任务落盘事实，全部落定或达到总上限后经宿主注入的汇报回调
//（Runtime.harvestReporter，见 WithHarvestReporter）回灌主会话，
// 以「系统收割」消息把结果写进上下文——此轮才告知 LLM 结果完成。
// 用户间隙继续与主 Agent 聊天不干扰：回灌与用户消息走同一串行队列。
const (
	// harvestPollInterval 轮询周期（20~30s 区间取中值）：第一次回来没查完
	// 就继续排下一轮，绝不一次不中就哑掉。
	harvestPollInterval = 25 * time.Second

	// harvestTotalCap 收割总上限（吸收原 collectOne 的止血上限语义）：
	// 到点必回灌收割报告（哪些完成/哪些超时），绝不静默消失。
	harvestTotalCap = 30 * time.Minute
)

// harvestTaskInfo 是收割目标的进程内登记信息（Submit 受理时写入）。
// daemon 重启即丢失——跨重启恢复依赖 LLM 回执中的 session_id 提示。
type harvestTaskInfo struct {
	sessionID string
	agentName string
	title     string
}

// harvestTarget 是单个收割目标：taskID（轮内句柄）+ sessionID（长期锚点，
// registry 优先、LLM 提示兜底）+ 展示信息（agentName/title 用于回灌报告）。
type harvestTarget struct {
	taskID    string
	sessionID string
	agentName string
	title     string
}

// StartHarvest 启动后台收割并立即返回（实现 tools.HarvestStarter）。
// 同一主会话已有收割在跑时返回 tools.ErrHarvestAlreadyActive（去重，
// 收集器对重复调用返回「进行中」回执，不重复收割、不重复回灌）。
func (m *subAgentManager) StartHarvest(mainSessionID string, targets []tools.HarvestTarget) error {
	m.mu.Lock()
	if _, active := m.activeHarvests[mainSessionID]; active {
		m.mu.Unlock()
		return tools.ErrHarvestAlreadyActive
	}
	m.activeHarvests[mainSessionID] = true
	m.mu.Unlock()

	hts := make([]harvestTarget, 0, len(targets))
	m.mu.RLock()
	for _, tgt := range targets {
		ht := harvestTarget{taskID: tgt.TaskID}
		// 会话锚点与展示信息：Submit 登记优先（进程内准确），LLM 的
		// session_id 提示兜底（跨重启恢复的落盘事实查询入口）。
		if info, ok := m.taskInfos[tgt.TaskID]; ok {
			ht.sessionID = info.sessionID
			ht.agentName = info.agentName
			ht.title = info.title
		}
		hts = append(hts, ht)
	}
	m.mu.RUnlock()
	for i := range hts {
		if hts[i].sessionID == "" {
			hts[i].sessionID = targets[i].SessionID
		}
	}

	m.rt.logger.Info("harvest: started",
		"main_session", mainSessionID,
		"count", len(hts),
		"poll_interval", harvestPollInterval.String(),
	)
	go m.runHarvest(mainSessionID, hts)
	return nil
}

// runHarvest 收割主循环：周期轮询各目标落定事实，全部落定或达到总上限
// 时汇报一次并结束。轮询依据（不需要 LLM 参与）：
//   - 主依据：控制平面（DefaultRuntimeManager）按句柄查运行实例终态；
//   - 落盘恢复：句柄不在控制平面（daemon 重启/终态条目被清理）时按
//     session_id 查落盘事实——会话 assistant 消息数超过收割启动时的
//     基线即有新产出，最后一条 assistant 消息即结果（daemon 重启不丢）；
//   - blocked（等 ask/permission）的子会话永远不会「完成」，收割不落定、
//     继续轮询，由总上限兜底回灌提示「某某在等你回答」，绝不无限等。
func (m *subAgentManager) runHarvest(mainSessionID string, targets []harvestTarget) {
	defer func() {
		if r := recover(); r != nil {
			m.rt.logger.Error("harvest goroutine panic", fmt.Errorf("%v", r),
				"main_session", mainSessionID, "stack", string(debug.Stack()))
		}
		// 无论成败解除去重登记：下一次 CollectResults 调用可重新启动收割
		m.mu.Lock()
		delete(m.activeHarvests, mainSessionID)
		m.mu.Unlock()
	}()

	deadline := time.Now().Add(harvestTotalCap)

	// 落盘恢复基线：有 sessionID 的目标快照当前 assistant 消息数；
	// -1 表示基线读取失败（该目标放弃落盘恢复路径）。
	baselines := make(map[string]int)
	if store := m.rt.sessionStore; store != nil {
		ctx := context.Background()
		for _, tgt := range targets {
			if tgt.sessionID == "" {
				continue
			}
			if _, snapshotted := baselines[tgt.sessionID]; snapshotted {
				continue
			}
			baselines[tgt.sessionID] = m.countAssistantMessages(ctx, store, tgt.sessionID)
		}
	}

	// statuses 与 blocked 都按 targets 索引对位：
	// statuses[i].Status == "" 表示尚未落定（继续轮询）。
	statuses := make([]tools.HarvestStatus, len(targets))
	blocked := make([]bool, len(targets))

	for {
		allSettled := true
		for i, tgt := range targets {
			if statuses[i].Status != "" {
				continue
			}
			st, settled := m.classifyTarget(tgt, baselines)
			blocked[i] = st.Status == "blocked"
			if settled {
				statuses[i] = st
			} else {
				allSettled = false
			}
		}
		if allSettled {
			m.reportHarvest(mainSessionID, statuses)
			return
		}
		if time.Now().After(deadline) {
			// 硬边界 2：超限必回灌——未落定目标按 blocked/timeout 标注，
			// 绝不静默消失。
			for i := range statuses {
				if statuses[i].Status != "" {
					continue
				}
				if blocked[i] {
					statuses[i] = tools.HarvestStatus{
						TaskID: targets[i].taskID, SessionID: targets[i].sessionID,
						AgentName: targets[i].agentName, Title: targets[i].title,
						Status: "blocked",
						Error:  fmt.Sprintf("收割总时长超过 %s 仍未落定：该子代理在等待你回答或授权，请到其会话中处理", harvestTotalCap),
					}
				} else {
					statuses[i] = tools.HarvestStatus{
						TaskID: targets[i].taskID, SessionID: targets[i].sessionID,
						AgentName: targets[i].agentName, Title: targets[i].title,
						Status: "timeout",
						Error:  fmt.Sprintf("收割总时长超过 %s 仍未落定：该子任务可能仍在后台执行或已挂死，可到其会话中查看进展", harvestTotalCap),
					}
				}
			}
			m.reportHarvest(mainSessionID, statuses)
			return
		}
		time.Sleep(harvestPollInterval)
	}
}

// classifyTarget 判定单个目标的当前状态。返回 (状态条目, 是否已落定)；
// 未落定时条目的 Status 为 running/blocked（仅用于 blocked 标记与调试）。
func (m *subAgentManager) classifyTarget(tgt harvestTarget, baselines map[string]int) (tools.HarvestStatus, bool) {
	st := tools.HarvestStatus{
		TaskID:    tgt.taskID,
		SessionID: tgt.sessionID,
		AgentName: tgt.agentName,
		Title:     tgt.title,
	}

	// 主依据：控制平面运行实例（受理登记 → runTask 结算，进程内必有条目）
	rt, ok := goagent.DefaultRuntimeManager().Get(tgt.taskID)
	if ok {
		switch rt.Status() {
		case goagent.StatusCompleted:
			st.Status = "completed"
			st.Result = rt.Result()
			return st, true
		case goagent.StatusPending, goagent.StatusRunning:
			if m.sessionBlocked(tgt.sessionID) {
				st.Status = "blocked"
				st.Error = "子代理在等待你回答或授权"
			} else {
				st.Status = "running"
			}
			return st, false
		default:
			// Failed / Cancelled 及其余终态
			reason := rt.Reason()
			if reason == "" {
				reason = rt.Status().String()
			}
			st.Status = "failed"
			st.Error = reason
			return st, true
		}
	}

	// 句柄不在控制平面：daemon 重启（进程内登记全丢）或终态条目被宿主
	// 清理。按 session_id 查落盘事实：assistant 消息数超过基线 = 有落盘
	// 新产出，最后一条 assistant 消息即结果（daemon 重启也不丢）。
	if tgt.sessionID == "" {
		st.Status = "failed"
		st.Error = "任务句柄不存在（可能 daemon 已重启），请对该 agent 重新派发 SubAgent 任务"
		return st, true
	}
	if store := m.rt.sessionStore; store != nil && baselines[tgt.sessionID] >= 0 {
		if result, found := m.lastAssistantAfter(context.Background(), store, tgt.sessionID, baselines[tgt.sessionID]); found {
			st.Status = "completed"
			st.Result = result + "\n（结果自会话落盘记录恢复）"
			return st, true
		}
	}
	// 会话无新增落盘产出：会话可能仍在执行（终态条目被清理）或已中断。
	// 不落定、继续轮询，由总上限兜底。
	st.Status = "running"
	return st, false
}

// sessionBlocked 判断子会话当前是否挂起等待用户回答（ask）或授权（permission）。
func (m *subAgentManager) sessionBlocked(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.states[sessionID]
	return st != nil && (st.pendingCh != nil || st.pendingAskCh != nil)
}

// countAssistantMessages 统计会话落盘的 assistant 消息数（基线快照）。
// 读取失败返回 -1（放弃该会话的落盘恢复路径）。
func (m *subAgentManager) countAssistantMessages(ctx context.Context, store session.SessionStore, sessionID string) int {
	msgs, err := store.Get(ctx, sessionID)
	if err != nil {
		return -1
	}
	count := 0
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			count++
		}
	}
	return count
}

// lastAssistantAfter 返回会话中第 after+1 条起的最后一条 assistant 消息内容
//（落盘恢复：assistant 消息数超过基线即有新产出）。无新产出返回 found=false。
func (m *subAgentManager) lastAssistantAfter(ctx context.Context, store session.SessionStore, sessionID string, after int) (string, bool) {
	msgs, err := store.Get(ctx, sessionID)
	if err != nil {
		return "", false
	}
	count := 0
	last := ""
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			count++
			last = msg.Content
		}
	}
	if count <= after || last == "" {
		return "", false
	}
	return last, true
}

// reportHarvest 把收割报告交给宿主注入的汇报回调（回灌主会话）。
// 未注入汇报回调（如 goharness 独立测试场景）时仅记日志，报告丢弃。
func (m *subAgentManager) reportHarvest(mainSessionID string, statuses []tools.HarvestStatus) {
	report := tools.HarvestReport{MainSessionID: mainSessionID, Targets: statuses}
	if m.rt.harvestReporter == nil {
		m.rt.logger.Warn("harvest: no reporter injected, report dropped",
			"main_session", mainSessionID, "count", len(statuses))
		return
	}
	m.rt.logger.Info("harvest: reporting",
		"main_session", mainSessionID, "count", len(statuses))
	m.rt.harvestReporter(report)
}

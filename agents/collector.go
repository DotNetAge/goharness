package agents

import (
	"fmt"

	"github.com/DotNetAge/goharness/tools"
)

// 收割意图登记（非阻塞 CollectResults 的受理端）。
//
// 设计定案（持久化收割）：subAgentManager 不再持有任何收割执行体——旧实现
// 的长命轮询 goroutine 无 supervisor、状态全在进程内存，goroutine 无声消失
// 或 daemon 重启后收割即蒸发（bega team 两次事故实证）。新实现只做两件事：
//  1. 按 taskInfos 登记表补全收割目标的会话锚点与展示信息（LLM 回执提示
//     session_id 兜底跨重启恢复）；
//  2. 经宿主注入的 tools.HarvestIntentRecorder 把收割意图落盘（svc 层写入
//     主会话目录 session-harvest.json）并立即返回回执。
//
// 落定判定、超时兜底与结果回灌全部由应用侧的无状态轮询器执行：每轮从磁盘
// 重新推导状态，任何一轮卡死或进程重启后下一轮自然恢复；回灌复用应用侧
// 后台执行链（会话串行队列，与用户消息天然顺序化）。

// harvestTaskInfo 是收割目标的受理登记信息（Submit 受理时写入 taskInfos）：
// 会话锚点（跨重启恢复的落盘事实查询入口）与回灌报告的展示信息。
// daemon 重启即丢失——跨重启恢复依赖 LLM 回执中的 session_id 提示。
type harvestTaskInfo struct {
	sessionID string
	agentName string
	title     string
}

// StartHarvest 登记收割意图并立即返回（实现 tools.HarvestStarter）。
// 去重键是任务句柄：已有意图时新增目标被落盘器按 task_id 合并登记（收割
// 在飞不丢任务）；全部目标均已登记时透传落盘器的 tools.ErrHarvestAlreadyActive
//（进程重启后依然有效），工具层对无新增的重复调用返回「进行中」回执。
func (m *subAgentManager) StartHarvest(mainSessionID string, targets []tools.HarvestTarget) error {
	if m.rt.harvestIntent == nil {
		err := fmt.Errorf("%s", "收割意图落盘器未注入（HarvestIntentRecorder），无法启动后台收割")
		m.rt.logger.Error("harvest: recorder not injected", err, "main_session", mainSessionID)
		return err
	}

	hts := make([]tools.HarvestTarget, len(targets))
	m.mu.RLock()
	for i, tgt := range targets {
		ht := tools.HarvestTarget{TaskID: tgt.TaskID, SessionID: tgt.SessionID}
		// 会话锚点与展示信息：Submit 登记优先（进程内准确），LLM 的
		// session_id 提示兜底（跨重启恢复的落盘事实查询入口）。
		if info, ok := m.taskInfos[tgt.TaskID]; ok {
			ht.SessionID = info.sessionID
			ht.AgentName = info.agentName
			ht.Title = info.title
		}
		hts[i] = ht
	}
	m.mu.RUnlock()

	if err := m.rt.harvestIntent(mainSessionID, hts); err != nil {
		return err
	}
	m.rt.logger.Info("harvest: intent recorded",
		"main_session", mainSessionID,
		"count", len(hts),
	)
	return nil
}

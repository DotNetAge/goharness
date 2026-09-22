package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/DotNetAge/goharness/tools"
)

// subagentWaitTimeout 是兜底自动等待子代理落定的上限。
// 与 CollectResults 的收集上限（tools.defaultCollectTimeout = 30 分钟）同级：
// 超时后返回已落定子代理的部分结果并注明仍在运行者，交由 LLM 决定收尾或重派。
const subagentWaitTimeout = 30 * time.Minute

// ActiveSponsored 返回指定主会话名下仍在运行的子会话 ID 列表。
// 读 sponsored 强停登记表（按主会话 ID 索引，正是「该主会话派生且未落定」
// 的视图；勿用 doneChs——它全局按子会话 ID 索引，无法按 sponsor 过滤）。
// 注意：spawn 结束（无论成败）即注销登记，已完成/已失败的子代理不出现在返回值中。
func (m *subAgentManager) ActiveSponsored(sponsorSessionID string) []string {
	if sponsorSessionID == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	subs, ok := m.sponsored[sponsorSessionID]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(subs))
	for id := range subs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// hasActiveSponsored 返回指定主会话名下是否仍有运行中的子代理。
// executor 在 finalize 前用做廉价预检（纯内存读），避免每轮收尾都白跑会话扫描。
func (m *subAgentManager) hasActiveSponsored(sponsorSessionID string) bool {
	if sponsorSessionID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sponsored[sponsorSessionID]) > 0
}

// waitAndCollect 兜底自动等待钩子（注入 AskBuilder.subagentWaitHook）：
// LLM 未调用 CollectResults 就试图以无工具调用响应收尾时，由 executor 在
// finalize 前调用。阻塞等待本会话名下全部子代理落定（带上限），收集结果
// 文本返回；ok=false 表示无需等待（无活跃子代理），调用方正常收尾。
//
// 收集范围 = 活跃子代理 ∪ 本回合派发过的子会话（从主会话消息扫描）：
// 后者覆盖「子代理在收尾前已完成并被注销登记」的场景——已完成的子代理
// 不在 sponsored 登记表中，但其结果同样未被收集，必须一并注入。
// 等待期间用户停止（ctx 取消）会立即唤醒，收集按取消语义短路。
func (m *subAgentManager) waitAndCollect(ctx context.Context, sponsorSessionID, question string) (string, bool) {
	active := m.ActiveSponsored(sponsorSessionID)
	if len(active) == 0 {
		return "", false
	}

	ids := mergeSessionIDs(active, m.scanSpawnedSessions(ctx, sponsorSessionID, question))

	// 等待全部落定（Promise.all 语义）：活跃子代理等待完成广播；
	// 已完成/跨进程恢复的会话无广播通道，立即通过。上限超时或用户
	// 停止（父 ctx 取消）均提前返回。
	waitCtx, cancel := context.WithTimeout(ctx, subagentWaitTimeout)
	defer cancel()
	spawnErrs := m.waitCompletions(waitCtx, ids)

	// 用户停止：不收集不注入，交由 executor 的 ctx 检查收尾（cancelled）。
	if ctx.Err() != nil {
		return "", true
	}

	timedOut := errors.Is(waitCtx.Err(), context.DeadlineExceeded)
	collected := m.collectSubResults(ctx, ids, spawnErrs, timedOut)
	if collected == "" {
		return "", true
	}
	return collected, true
}

// mergeSessionIDs 合并去重活跃子代理与本回合扫描到的子会话 ID（排序稳定输出）。
func mergeSessionIDs(active, scanned []string) []string {
	seen := make(map[string]struct{}, len(active)+len(scanned))
	ids := make([]string, 0, len(active)+len(scanned))
	for _, id := range append(append([]string{}, active...), scanned...) {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// scanSpawnedSessions 从主会话消息中扫描本回合派发的子会话 ID。
// 回合边界：以内容等于 question 的最后一条 user 消息为起点（exec 在本回合
// 开始时追加的用户问题），其后 SubAgent 工具结果携带的 session_id 均属本回合，
// 之前回合的派发记录不重复收集（其结果早已注入或收集过）。
// 兜底：找不到匹配的 user 消息（魔法词轮次等）或会话读取失败时返回空——
// 此时仅依赖 ActiveSponsored 的活跃视图。
func (m *subAgentManager) scanSpawnedSessions(ctx context.Context, sponsorSessionID, question string) []string {
	if question == "" || m.rt.sessionStore == nil {
		return nil
	}
	msgs, err := m.rt.sessionStore.Get(ctx, sponsorSessionID)
	if err != nil {
		return nil
	}

	// 定位本回合起点：最后一条内容匹配的用户问题消息。
	start := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content == question {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}

	// 顺序扫描：assistant 消息记录 SubAgent 工具调用的 tool_call_id，
	// 随后配对的 tool 消息（结果 JSON）解析出 session_id。
	pendingCalls := make(map[string]struct{})
	subIDs := make(map[string]struct{})
	for i := start; i < len(msgs); i++ {
		msg := msgs[i]
		switch msg.Role {
		case "assistant":
			for _, tc := range msg.ToolCalls {
				if tc.Name == "SubAgent" {
					pendingCalls[tc.ID] = struct{}{}
				}
			}
		case "tool":
			if _, ok := pendingCalls[msg.ToolCallID]; !ok {
				continue
			}
			// SubAgent 工具结果为 {"status":"running","agent_name":...,"session_id":...} JSON。
			// spawn 是异步的，结果在 spawn 结束前始终记录为 running，但 session_id
			// 在同步阶段（ensureSession 存根）已确定，可直接采用。
			var payload struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(msg.Content), &payload); err == nil && payload.SessionID != "" {
				subIDs[payload.SessionID] = struct{}{}
			}
		}
	}

	ids := make([]string, 0, len(subIDs))
	for id := range subIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// subResultEntry 是兜底收集结果的单条条目，字段与 CollectResults 的输出对齐，
// 使 LLM 看到的结果形态与显式调用 CollectResults 时一致。
type subResultEntry struct {
	SessionID string `json:"session_id"`
	AgentName string `json:"agent_name,omitempty"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// collectSubResults 逐个收集子会话结果，返回注入主循环的 user 消息文本。
// 判定复用 tools.FindFinalAnswer（与 CollectResults 轮询完全一致）：
// 最终答案 → completed；终止标记 → failed；空 → 等待超时标注仍在执行。
func (m *subAgentManager) collectSubResults(ctx context.Context, ids []string, spawnErrs map[string]error, timedOut bool) string {
	if len(ids) == 0 {
		return ""
	}
	entries := make([]subResultEntry, 0, len(ids))
	for _, id := range ids {
		entry := subResultEntry{SessionID: id, Status: "failed"}
		// spawn 早期失败（无子会话可写标记）：等待已确保广播结束，直接上报失败原因。
		if err, ok := spawnErrs[id]; ok {
			entry.Error = fmt.Sprintf("子代理任务启动失败: %v", err)
			entries = append(entries, entry)
			continue
		}
		entry.AgentName = m.lookupAgentName(ctx, id)

		msgs, err := m.rt.sessionStore.Get(ctx, id)
		if err != nil {
			entry.Error = fmt.Sprintf("读取子会话消息失败: %v", err)
			entries = append(entries, entry)
			continue
		}
		answer, termReason := tools.FindFinalAnswer(msgs)
		switch {
		case answer != "":
			entry.Status = "completed"
			entry.Result = answer
		case termReason != "":
			entry.Error = "子代理已终止但未产生最终答案: " + termReason
		case timedOut:
			entry.Status = "running"
			entry.Error = fmt.Sprintf("等待超时（上限 %s）：子代理仍在执行，未产出结果", subagentWaitTimeout)
		default:
			// 非超时且无答案/标记：广播已结束但会话无结果（异常），按失败上报。
			entry.Error = "子代理已结束但未产出结果"
		}
		entries = append(entries, entry)
	}

	payload, err := json.Marshal(entries)
	if err != nil {
		m.rt.logger.Error("兜底收集子代理结果序列化失败", err, "count", len(ids))
		return ""
	}
	return "以下是主回合等待全部子代理任务落定后自动收集的结果（本次收尾未经 CollectResults 工具，由运行时兜底等待并收集）。" +
		"请基于这些结果继续作答；status=running 表示等待超时仍未完成，如需其结果请重新派发或继续等待：\n" + string(payload)
}

// lookupAgentName 从子会话元数据读取 agent 名（与 CollectResults 的
// lookupSubAgentName 同源：优先 GetMeta，读不到留空即可，不阻塞收集）。
func (m *subAgentManager) lookupAgentName(ctx context.Context, sessionID string) string {
	if m.rt.sessionStore == nil {
		return ""
	}
	info, err := m.rt.sessionStore.GetMeta(ctx, sessionID)
	if err != nil || info == nil {
		return ""
	}
	return info.AgentName
}

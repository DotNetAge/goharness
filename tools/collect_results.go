package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/session"
)

const (
	// defaultCollectTimeout 是等待所有子代理完成的最大超时时间。
	defaultCollectTimeout = 30 * time.Minute
	// pollInterval 是子 session 轮询之间的间隔时间。
	pollInterval = 2 * time.Second

	// SubAgentTerminatedPrefix 是子智能体无最终答案终止时的标记前缀。
	// subAgentManager.spawn 在子会话无最终答案（授权超时、上下文取消、执行错误等）
	// 时追加一条以该前缀开头的 assistant 消息，
	// CollectResults 据此快速判定失败，避免死等到默认 30 分钟收集超时。
	SubAgentTerminatedPrefix = "[sub-agent-terminated]"

	// SubAgentTaskStartPrefix 是子智能体任务开始标记的前缀。
	// subAgentManager.spawn 在复用已有会话（延续上下文）时，于新任务的问题消息之前
	// 追加一条以该前缀开头的 user 消息，标记新任务的起点。
	// findFinalAnswer 据此划定任务边界：该消息（及更早的 user 消息）之前的
	// assistant 消息属于历史任务，不得作为当前任务的结果——
	// 否则会话复用后 CollectResults 会提前命中旧任务的答案或终止标记。
	SubAgentTaskStartPrefix = "[sub-agent-task-start]"
)

// CollectResultsTool 收集子代理任务的结果。
// 通过轮询子 session 获取 SubAgent 的执行结果，不依赖 ResultStore。
type CollectResultsTool struct{}

func NewCollectResultsTool() *CollectResultsTool {
	return &CollectResultsTool{}
}

func (t *CollectResultsTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "CollectResults",
		MaxResultSizeChars: 50000,
		Description:        "收集子代理任务的结果",
		Prompt: `收集子代理任务的结果。支持重试与恢复。

返回 JSON 数组，每项包含 {session_id, agent_name, status, result}。

当存在子代理 session_id 时，请优先调用此工具：
- 首次调用：等待正在运行的任务并返回结果
- 重试：从已完成的任务中恢复结果（从子 session 直接读取）

如果返回 status=completed，result 字段包含子代理的最终答案。
如果某个 session_id 未返回结果（missing/failed），请对该 agent 重新发起
SubAgent 调用（task 设为"继续之前的任务并给出最终结果"），
再调用 CollectResults 获取新结果。同一 agent 会复用之前的 session。`,
		Tags:         []string{"orchestration", "collect", "result"},
		IsIdempotent: true,
		Parameters: []Parameter{
			{Name: "session_ids", Type: "array", Description: "要收集结果的子 session ID 数组。session_id 来自 SubAgent 返回结果。", Required: true},
		},
	}
}

func (t *CollectResultsTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	logger := getLogger(ctx)
	if tc == nil || tc.Session == nil || tc.SessionStore == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("CollectResults", "包含 Session 和 SessionStore 的 ToolContext"))
	}

	rawIDsVal, found := GetParam(params, "session_ids")
	if !found {
		return nil, fmt.Errorf("%s", GuideMissingParam("CollectResults", "session_ids"))
	}
	rawIDs, ok := rawIDsVal.([]any)
	if !ok {
		return nil, fmt.Errorf("%s", GuideWrongParamType("CollectResults", "session_ids", "array", rawIDsVal))
	}

	sessionIDs := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		if id, ok := raw.(string); ok {
			sessionIDs = append(sessionIDs, id)
		}
	}
	logger.Info("collect_results: collecting results",
		"session_ids", sessionIDs,
		"count", len(sessionIDs),
		"deadline_in", defaultCollectTimeout.String(),
	)

	// 去除调用方的超时限制，以允许等待长时间运行的子代理完成
	waitCtx := context.WithoutCancel(ctx)

	deadline := time.Now().Add(defaultCollectTimeout)

	var jsonResults []map[string]string
	for _, id := range sessionIDs {
		result := t.pollForResult(waitCtx, tc, id, deadline)
		if result != nil {
			jsonResults = append(jsonResults, result)
		} else {
			logger.Warn("collect_results: sub-agent did not complete within deadline",
				"session_id", id,
				"deadline_minutes", defaultCollectTimeout.Minutes(),
			)
			jsonResults = append(jsonResults, map[string]string{
				"session_id": id,
				"status":     "failed",
				"error":      "超时：子代理未在指定时间内完成",
			})
		}
	}

	out, err := json.Marshal(jsonResults)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", BuildGuide("序列化收集结果时失败", WithErrDetail("结果数据包含无法序列化的内容（如非法值或循环引用）", err), "检查收集到的子代理结果数据，剔除无法序列化的字段后重试"), err)
	}
	return string(out), nil
}

// pollForResult 轮询等待单个子 session 的结果。
// 直接通过 session_id 加载子 session → 查找 FinalAnswer。
// 每 30 次轮询输出一次进度日志，避免高频重复日志。
func (t *CollectResultsTool) pollForResult(ctx context.Context, tc *ToolContext, sessionID string, deadline time.Time) map[string]string {
	logger := getLogger(ctx)
	startedAt := time.Now()
	pollCount := 0

	logger.Info("collect_results: start polling sub-session",
		"session_id", sessionID,
		"deadline_at", deadline.Format(time.RFC3339),
	)

	for {
		select {
		case <-ctx.Done():
			logger.Warn("collect_results: poll cancelled",
				"session_id", sessionID,
				"poll_count", pollCount,
				"elapsed_ms", time.Since(startedAt).Milliseconds(),
				"reason", ctx.Err(),
			)
			return nil
		default:
		}

		if time.Now().After(deadline) {
			logger.Warn("collect_results: deadline reached",
				"session_id", sessionID,
				"poll_count", pollCount,
				"elapsed_ms", time.Since(startedAt).Milliseconds(),
			)
			return nil
		}

		// 加载子 session 消息
		subMsgs, err := tc.SessionStore.Get(ctx, sessionID)
		if err != nil || len(subMsgs) == 0 {
			if pollCount%30 == 0 {
				logger.Info("collect_results: sub-session not ready yet, retrying",
					"session_id", sessionID,
					"poll_count", pollCount,
					"elapsed_s", int(time.Since(startedAt).Seconds()),
					"error", err,
				)
			}
			pollCount++
			time.Sleep(pollInterval)
			continue
		}

		// 查找执行结果：正常最终答案，或终止标记（无最终答案的失败终止）。
		answer, termReason := findFinalAnswer(subMsgs)
		if answer != "" {
			agentName := lookupSubAgentName(ctx, tc, sessionID)
			logger.Info("collect_results: found FinalAnswer in sub-session",
				"session_id", sessionID,
				"agent_name", agentName,
				"poll_count", pollCount,
				"elapsed_ms", time.Since(startedAt).Milliseconds(),
				"result_len", len(answer),
			)
			return map[string]string{
				"session_id": sessionID,
				"agent_name": agentName,
				"status":     "completed",
				"result":     answer,
			}
		}
		if termReason != "" {
			// 子会话已终止但未产生最终答案（授权超时、上下文取消等）：
			// 立即判定失败返回，不再继续轮询。
			agentName := lookupSubAgentName(ctx, tc, sessionID)
			logger.Warn("collect_results: sub-session terminated without FinalAnswer",
				"session_id", sessionID,
				"agent_name", agentName,
				"poll_count", pollCount,
				"elapsed_ms", time.Since(startedAt).Milliseconds(),
				"reason", termReason,
			)
			return map[string]string{
				"session_id": sessionID,
				"agent_name": agentName,
				"status":     "failed",
				"error":      "子代理已终止但未产生最终答案: " + termReason,
			}
		}

		// 每 30 次轮询输出一次进度，避免高频重复日志
		if pollCount%30 == 0 {
			logger.Info("collect_results: no FinalAnswer yet, retrying",
				"session_id", sessionID,
				"poll_count", pollCount,
				"elapsed_s", int(time.Since(startedAt).Seconds()),
			)
		}
		pollCount++
		time.Sleep(pollInterval)
	}
}

// lookupSubAgentName 从子 session 的 meta 中获取 agent_name。
// 优先使用 SessionStore.GetMeta（从文件元数据读取），
// 兜底从子 session 的 system 消息解析。
func lookupSubAgentName(ctx context.Context, tc *ToolContext, sessionID string) string {
	if tc.SessionStore != nil {
		info, err := tc.SessionStore.GetMeta(ctx, sessionID)
		if err == nil && info != nil && info.AgentName != "" {
			return info.AgentName
		}
	}
	// 兜底：从 system 消息解析
	return extractAgentNameFromMessages(tc, sessionID)
}

// extractAgentNameFromMessages 从子 session 的 system 消息中提取 agent_name。
func extractAgentNameFromMessages(tc *ToolContext, sessionID string) string {
	msgs, err := tc.SessionStore.Get(context.Background(), sessionID)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Role == "system" && m.Content != "" {
			var meta struct {
				AgentName string `json:"agent_name"`
			}
			if err := json.Unmarshal([]byte(m.Content), &meta); err == nil && meta.AgentName != "" {
				return meta.AgentName
			}
		}
	}
	return ""
}

// findFinalAnswer 在子 session 的消息中查找执行结果。
// 返回两个值：
//   - answer：子会话正常完成时的最终答案（当前任务段内最后一条无 tool_calls 的 assistant 消息内容）。
//   - termReason：若当前任务段内最后一条无 tool_calls 的 assistant 消息是终止标记
//     （子会话无最终答案即终止，如授权超时、上下文取消），返回标记携带的终止原因。
//
// 终止标记消息同时满足 assistant / 无 tool_calls / 内容非空 三个条件，
// 因此必须先识别标记再按正常答案处理，避免把标记误判为最终答案。
//
// 任务边界（会话复用）：子会话会被复用（同 Agent + ProjectDir + Sponsor 的空闲会话
// 延续上下文），历史任务的最终答案与终止标记仍保留在会话消息中。若不加边界判断，
// 新任务尚未完成时 CollectResults 会提前命中旧任务的结果。因此：
//   - spawn 复用会话时追加一条 user 角色任务开始标记（SubAgentTaskStartPrefix）；
//   - 本函数从后向前扫描，遇到最近的 user 消息（任务开始标记、任务问题或图片消息）
//     即停止——该消息属于当前任务的起点及边界，之前的 assistant 消息属于历史任务，
//     一律不计入当前任务结果，返回空结果让调用方继续轮询。
func findFinalAnswer(msgs []session.Message) (answer, termReason string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "user" {
			// 任务边界：此消息（含）之后才可能属于当前任务。
			return "", ""
		}
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && m.Content != "" {
			if strings.HasPrefix(m.Content, SubAgentTerminatedPrefix) {
				reason := strings.TrimSpace(strings.TrimPrefix(m.Content, SubAgentTerminatedPrefix))
				return "", reason
			}
			return m.Content, ""
		}
	}
	return "", ""
}

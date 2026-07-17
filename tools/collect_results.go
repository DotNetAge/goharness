package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DotNetAge/goharness/session"
)

const (
	// defaultCollectTimeout is the maximum time to wait for all sub-agents to complete.
	defaultCollectTimeout = 30 * time.Minute
	// pollInterval is the time between sub-session polls.
	pollInterval = 2 * time.Second
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
再调用 CollectResults 获取新结果。同一 agent 会复用之前的 session。

注意：session_id 来自 SubAgent 返回结果中的 session_id 字段。`,
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
		return nil, fmt.Errorf("collect_results 工具需要包含 Session 和 SessionStore 的 ToolContext")
	}

	rawIDsVal, found := GetParam(params, "session_ids")
	if !found {
		return nil, fmt.Errorf("session_ids 必须是字符串数组")
	}
	rawIDs, ok := rawIDsVal.([]any)
	if !ok {
		return nil, fmt.Errorf("session_ids 必须是字符串数组")
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

	// Strip caller's timeout to allow waiting for long-running SubAgents
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
		return nil, fmt.Errorf("序列化收集结果失败：%w", err)
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

		// 查找 FinalAnswer
		if answer := findFinalAnswer(subMsgs); answer != "" {
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

// findFinalAnswer 在子 session 的消息中查找 FinalAnswer。
// FinalAnswer 是最后一条没有 tool_calls 的 assistant 消息。
func findFinalAnswer(msgs []session.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

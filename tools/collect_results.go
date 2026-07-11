package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// CollectResultsTool blocks until the specified tasks complete and returns all results.
// It is sync (IsAsync=false) — its goroutine is blocked internally via ResultStore.WaitForResult.
type CollectResultsTool struct{}

func NewCollectResultsTool() *CollectResultsTool {
	return &CollectResultsTool{}
}

func (t *CollectResultsTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "CollectResults",
		MaxResultSizeChars: 50000,
		Description:        "Wait for one or more async tasks to complete and return their results.",
		Prompt: `Collect results from SubAgent tasks. Supports both first-time collection and recovery on retry.

Call this tool first whenever SubAgent task_ids exist:
- First call: waits for running tasks and returns results
- Retry: recovers results from previously completed tasks

Do NOT re-spawn a SubAgent for an existing task_id — retry CollectResults instead.`,
		Tags:         []string{"orchestration", "collect", "result"},
		IsIdempotent: true,
		Parameters: []Parameter{
			{Name: "task_ids", Type: "array", Description: "Array of task IDs to collect results from.", Required: true},
		},
	}
}

func (t *CollectResultsTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.ResultStore == nil {
		return nil, fmt.Errorf("collect_results tool requires ToolContext with ResultStore")
	}

	rawIDs, ok := params["task_ids"].([]any)
	if !ok {
		return nil, fmt.Errorf("task_ids must be an array of strings")
	}

	// Strip the caller's syncTimeout deadline (default 5min) to allow waiting
	// for long-running SubAgents. The ResultStore's own 30min default timeout
	// and explicit context cancellation still apply.
	waitCtx := context.WithoutCancel(ctx)

	var jsonResults []map[string]string
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("task_id must be a string, got %T", raw)
		}

		// Phase 1: Non-blocking Get (fast path — SubAgent already completed)
		r := tc.ResultStore.Get(id)

		// Phase 2: Chain info fallback (covers daemon restart — ResultStore empty
		// but SubAgent completed and wrote chain info to parent session before crash)
		if r == nil || !r.Done {
			if entry := fallbackCollect(tc, id); entry != nil {
				jsonResults = append(jsonResults, entry)
				continue
			}
		}

		// Phase 3: Blocking wait (SubAgent still running)
		if r == nil || !r.Done {
			r = tc.ResultStore.WaitForResult(waitCtx, id)
		}

		// Phase 4: Process result
		if r.Error != "" {
			// WaitForResult failed — try chain info fallback one more time
			// (may call ResumeFunc to restart an interrupted SubAgent)
			if entry := fallbackCollect(tc, id); entry != nil {
				jsonResults = append(jsonResults, entry)
				continue
			}
			jsonResults = append(jsonResults, map[string]string{
				"task_id":    id,
				"session_id": r.SessionID,
				"status":     "failed",
				"error":      r.Error,
			})
		} else {
			jsonResults = append(jsonResults, map[string]string{
				"task_id":    id,
				"session_id": r.SessionID,
				"status":     "completed",
				"result":     r.Result,
			})
		}
	}

	out, err := json.Marshal(jsonResults)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal collect results: %w", err)
	}
	return string(out), nil
}

// fallbackCollect attempts to recover a SubAgent result from the parent session's
// chain info and the sub-session's persisted messages.
//
// This handles the daemon restart / session recovery case where ResultStore is
// empty but the SubAgent had already completed and its chain info was written
// to the parent session.
//
// Returns nil if no chain info is found (caller should continue with WaitForResult).
// When chain info exists but no FinalAnswer is found, it attempts ResumeFunc to
// restart the interrupted SubAgent.
func fallbackCollect(tc *ToolContext, taskID string) map[string]string {
	if tc.Session == nil || tc.SessionStore == nil {
		return nil
	}

	// 1. Scan parent session for chain info matching task_id
	var chainSessionID, chainAgent, chainStatus string
	for _, msg := range tc.Session.All() {
		if msg.Role != "tool" {
			continue
		}
		var chain map[string]string
		if err := json.Unmarshal([]byte(msg.Content), &chain); err != nil {
			continue
		}
		if chain["type"] != "chain" || chain["task_id"] != taskID {
			continue
		}
		chainSessionID = chain["session_id"]
		chainAgent = chain["agent"]
		chainStatus = chain["status"]
		break
	}
	if chainSessionID == "" {
		return nil // No chain info — SubAgent may still be running
	}

	// 2. Load sub-session messages
	subMsgs, err := tc.SessionStore.Get(context.Background(), chainSessionID)
	if err != nil || len(subMsgs) == 0 {
		return nil
	}

	// 3. Find FinalAnswer (last assistant message without tool_calls)
	var finalAnswer string
	for i := len(subMsgs) - 1; i >= 0; i-- {
		m := subMsgs[i]
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && m.Content != "" {
			finalAnswer = m.Content
			break
		}
	}

	if finalAnswer != "" {
		// Found FinalAnswer → return result
		return map[string]string{
			"task_id":    taskID,
			"session_id": chainSessionID,
			"agent":      chainAgent,
			"status":     "completed",
			"result":     finalAnswer,
		}
	}

	// 4. No FinalAnswer — chain info says "failed" or SubAgent was interrupted
	if chainStatus == "failed" {
		return map[string]string{
			"task_id":    taskID,
			"session_id": chainSessionID,
			"agent":      chainAgent,
			"status":     "failed",
			"error":      "sub-agent failed and no result available",
		}
	}

	// 5. SubAgent interrupted (chain shows completed but no FinalAnswer found,
	//    or chain status is unknown) — try ResumeFunc
	if tc.ResumeFunc != nil {
		result, resumeErr := tc.ResumeFunc(context.Background(), chainSessionID)
		if resumeErr == nil {
			return map[string]string{
				"task_id":    taskID,
				"session_id": chainSessionID,
				"agent":      chainAgent,
				"status":     "completed",
				"result":     result,
			}
		}
		return map[string]string{
			"task_id":    taskID,
			"session_id": chainSessionID,
			"agent":      chainAgent,
			"status":     "failed",
			"error":      fmt.Sprintf("resume sub-agent failed: %v", resumeErr),
		}
	}

	return map[string]string{
		"task_id":    taskID,
		"session_id": chainSessionID,
		"agent":      chainAgent,
		"status":     "failed",
		"error":      "sub-agent interrupted and no ResumeFunc available",
	}
}

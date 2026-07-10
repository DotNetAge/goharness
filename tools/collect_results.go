package tools

import (
	"context"
	"fmt"
	"strings"
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
		Prompt: `Block until the specified async tasks (from SubAgent) complete, then return all results.

Pass task_ids from previous SubAgent calls. Multiple results can be collected in one call.`,
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

	var results []string
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("task_id must be a string, got %T", raw)
		}
		r := tc.ResultStore.WaitForResult(waitCtx, id)
		if r.Error != "" {
			if r.SessionID != "" {
				results = append(results, fmt.Sprintf("[%s] failed (session:%s): %s", id, r.SessionID, r.Error))
			} else {
				results = append(results, fmt.Sprintf("[%s] failed: %s", id, r.Error))
			}
		} else {
			if r.SessionID != "" {
				results = append(results, fmt.Sprintf("[%s] completed (session:%s):\n%s", id, r.SessionID, r.Result))
			} else {
				results = append(results, fmt.Sprintf("[%s] completed:\n%s", id, r.Result))
			}
		}
	}

	return strings.Join(results, "\n---\n"), nil
}

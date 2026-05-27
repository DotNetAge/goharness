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
		Description:        "Wait for one or more async tasks to complete and return their results. Blocks until all specified tasks are done.",
		Prompt: `Wait for async tasks (started by SubAgent) to finish and collect their results.

This tool blocks until ALL specified task_ids have completed. Use it after SubAgent() to retrieve results.

Behavior:
- Takes a list of task_ids from previous SubAgent calls
- Blocks until every task is complete
- Returns all results at once
- Progress events are emitted as each task completes

Usage:
- Pass task_ids returned by SubAgent() calls
- Multiple parallel sub-agent results can be collected in one call
- Sequential tasks should be chained: SubAgent → Collect → SubAgent → Collect`,
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

	var results []string
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("task_id must be a string, got %T", raw)
		}
		r := tc.ResultStore.WaitForResult(id)
		if r.Error != "" {
			results = append(results, fmt.Sprintf("[%s] failed: %s", id, r.Error))
		} else {
			results = append(results, fmt.Sprintf("[%s] completed:\n%s", id, r.Result))
		}
	}

	return strings.Join(results, "\n---\n"), nil
}

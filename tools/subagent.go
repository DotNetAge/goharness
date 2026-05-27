package tools

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DotNetAge/goreact/events"
	"github.com/DotNetAge/goreact/store"
)

var subagentSem = make(chan struct{}, 20)

// SpawnFunc creates and runs a sub-agent for a delegated task.
// Returns the sub-agent's result and any error.
type SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

// SubAgentTool lets the LLM spawn sub-agents for delegated tasks.
// IsAsync=true — returns {task_id, status: "running"} immediately.
// Results are collected via CollectResultsTool (reads from ResultStore).
type SubAgentTool struct {
	spawn   SpawnFunc
	counter atomic.Int64
}

// NewSubAgentTool creates a SubAgentTool.
// spawn is provided by the reactor to avoid circular imports.
func NewSubAgentTool(spawn SpawnFunc) *SubAgentTool {
	return &SubAgentTool{spawn: spawn}
}

func (t *SubAgentTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "SubAgent",
		Description: "Spawn a sub-agent for a task. Returns immediately with a task_id. Use CollectResults to retrieve the result later.",
		Prompt: `Spawn a sub-agent to handle a task. Use this for two scenarios:

1. **Expertise handoff** — the task falls outside your role, and a specialist agent is better suited.
2. **Parallelization** — the workload is large and can be split into independent sub-tasks that run concurrently to save time.

Returns {task_id, status: "running"} immediately. The actual result must be collected later using the CollectResults tool.

When to spawn:
- The task is outside your defined area of expertise — do your own work first.
- A specialist agent exists whose role matches the task.
- The user explicitly asks for another agent to handle the task.
- The task has many independent parts — spawn multiple sub-agents in parallel to finish faster.

Usage:
- Name the sub-agent based on its role (e.g., "code_reviewer", "data_analyst") or reuse your own role for parallel workers.
- The task description should be clear and self-contained.
- Multiple SubAgent calls in the same Act phase run in parallel.
- For sequential sub-tasks, call SubAgent one per round, waiting for CollectResults between rounds.

Don't race: After spawning a sub-agent, you know nothing about what it found until you call CollectResults.`,
		Tags:    []string{"orchestration", "subagent", "sub-agent"},
		IsAsync: true,
		Parameters: []Parameter{
			{Name: "agent_name", Type: "string", Description: "Name of the sub-agent to spawn.", Required: true},
			{Name: "task", Type: "string", Description: "Task description for the sub-agent.", Required: true},
		},
	}
}

func (t *SubAgentTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	agentName, _ := params["agent_name"].(string)
	if agentName == "" {
		return nil, fmt.Errorf("agent_name is required")
	}
	task, _ := params["task"].(string)
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	if tc == nil || tc.EmitEvent == nil {
		return nil, fmt.Errorf("subagent tool requires ToolContext with EventBus")
	}
	if t.spawn == nil {
		return nil, fmt.Errorf("subagent tool: SpawnFunc not configured")
	}

	logger.Info("spawning sub-agent",
		"agent_name", agentName,
		"task", truncateForLog(task, 100),
	)

	taskID := fmt.Sprintf("subagent-%d", t.counter.Add(1))

	tc.EmitEvent(events.ReactEvent{
		AgentID: "main",
		Type:    events.SubtaskSpawned,
		Data:    map[string]any{"task_id": taskID, "agent_name": agentName, "task": task},
	})

	// Run sub-agent in background
	subagentSem <- struct{}{}
	go func() {
		startedAt := time.Now()
		defer func() { <-subagentSem }()

		result, err := t.spawn(ctx, agentName, task)
		completedAt := time.Now()

		if err != nil {
			logger.Error("sub-agent task failed", err,
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
			)
		} else {
			logger.Info("sub-agent task completed",
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
				"result_len", len(result),
			)
		}

		var taskResult *store.TaskResult
		if err != nil {
			taskResult = &store.TaskResult{TaskID: taskID, Error: err.Error(), Done: true}
		} else {
			taskResult = &store.TaskResult{TaskID: taskID, Result: result, Done: true}
		}
		if tc.ResultStore != nil {
			tc.ResultStore.Store(taskID, taskResult)
		}
		tc.EmitEvent(events.ReactEvent{
			AgentID: agentName,
			Type:    events.SubtaskCompleted,
			Data:    map[string]any{"task_id": taskID, "success": err == nil},
		})
	}()

	return map[string]any{
		"task_id":    taskID,
		"status":     "running",
		"agent_name": agentName,
	}, nil
}

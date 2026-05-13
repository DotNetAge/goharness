package tools

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// SpawnFunc creates and runs a sub-agent for a delegated task.
// Returns the sub-agent's result and any error.
type SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

// DelegateTool lets the LLM delegate tasks to sub-agents.
// It is async (IsAsync=true) — returns {task_id, status: "running"} immediately.
// Now also persists task state in KVStore for unified tracking with Task tools.
type DelegateTool struct {
	spawn     SpawnFunc
	counter   atomic.Int64
}

// NewDelegateTool creates a DelegateTool.
// spawn is provided by the reactor to avoid circular imports.
func NewDelegateTool(spawn SpawnFunc) *DelegateTool {
	return &DelegateTool{spawn: spawn}
}

func (t *DelegateTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "Delegate",
		Description: "Delegate a task to a sub-agent. Returns immediately with a task_id. Use CollectResults to retrieve the result later.",
		Prompt: `Dispatch a task to another agent. Use this for two scenarios:

1. **Expertise handoff** — the task falls outside your role, and a specialist agent is better suited.
2. **Parallelization** — the workload is large and can be split into independent sub-tasks that run concurrently to save time.

Returns {task_id, status: "running"} immediately. The actual result must be collected later using the CollectResults tool.

When to delegate:
- The task is outside your defined area of expertise — do your own work first.
- A specialist agent exists whose role matches the task.
- The user explicitly asks for another agent to handle the task.
- The task has many independent parts — spawn multiple agents in parallel to finish faster.

Usage:
- Name the sub-agent based on its role (e.g., "code_reviewer", "data_analyst") or reuse your own role for parallel workers.
- The task description should be clear and self-contained.
- Multiple delegates called in the same Act phase run in parallel.
- For sequential sub-tasks, call delegate one per round, waiting for CollectResults between rounds.

Don't race: After launching a delegate, you know nothing about what the sub-agent found until you call CollectResults.`,
		Tags:    []string{"orchestration", "delegate", "sub-agent"},
		IsAsync: true,
		Parameters: []core.Parameter{
			{Name: "agent_name", Type: "string", Description: "Name of the sub-agent to delegate to.", Required: true},
			{Name: "task", Type: "string", Description: "Task description for the sub-agent.", Required: true},
		},
	}
}

func (t *DelegateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	agentName, _ := params["agent_name"].(string)
	if agentName == "" {
		return nil, fmt.Errorf("agent_name is required")
	}
	task, _ := params["task"].(string)
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	logger := getLogger(ctx)

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.EmitEvent == nil {
		return nil, fmt.Errorf("delegate tool requires ToolContext with EventBus")
	}
	if t.spawn == nil {
		return nil, fmt.Errorf("delegate tool: SpawnFunc not configured")
	}

	logger.Info("delegating task to sub-agent",
		"agent_name", agentName,
		"task", truncateForLog(task, 100),
	)

	// Use unified task ID format
	taskID := fmt.Sprintf("task-%d", t.counter.Add(1))

	// Persist task in KVStore for unified tracking with Task tools
	if tc.SessionID != "" && tc.KVStore != nil {
		taskRecord := &Task{
			ID:          taskID,
			Type:        TaskTypeAgent,
			Description: task,
			Status:      TaskPending,
			AgentName:   agentName,
			Prompt:      task,
		}
		_ = CreateTask(ctx, tc.SessionID, taskRecord)
	}

	// Emit event notification
	tc.EmitEvent(core.ReactEvent{
		AgentID: "main",
		Type:    core.SubtaskSpawned,
		Data:    map[string]any{"task_id": taskID, "agent_name": agentName, "task": task},
	})

	// Run sub-agent in background
	go func() {
		localTask := &Task{
			ID:          taskID,
			Type:        TaskTypeAgent,
			Description: task,
			Status:      TaskRunning,
			AgentName:   agentName,
			Prompt:      task,
		}
		now := time.Now()
		localTask.StartedAt = &now
		if tc.SessionID != "" && tc.KVStore != nil {
			_ = UpdateTask(ctx, tc.SessionID, localTask)
		}

		result, err := t.spawn(ctx, agentName, task)
		completedAt := time.Now()
		localTask.CompletedAt = &completedAt
		if err != nil {
			localTask.Status = TaskFailed
			localTask.Error = err.Error()
			logger.Error("sub-agent task failed", err,
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(*localTask.StartedAt).Milliseconds(),
			)
		} else {
			localTask.Status = TaskCompleted
			localTask.Result = result
			logger.Info("sub-agent task completed",
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(*localTask.StartedAt).Milliseconds(),
				"result_len", len(result),
			)
		}
		if tc.SessionID != "" && tc.KVStore != nil {
			_ = UpdateTask(ctx, tc.SessionID, localTask)
		}

		var taskResult *core.TaskResult
		if err != nil {
			taskResult = &core.TaskResult{TaskID: taskID, Error: err.Error(), Done: true}
		} else {
			taskResult = &core.TaskResult{TaskID: taskID, Result: result, Done: true}
		}
		if tc.ResultStore != nil {
			tc.ResultStore.Store(taskID, taskResult)
		}
		tc.EmitEvent(core.ReactEvent{
			AgentID: agentName,
			Type:    core.SubtaskCompleted,
			Data:    map[string]any{"task_id": taskID, "success": err == nil},
		})
	}()

	return map[string]any{
		"task_id":    taskID,
		"status":     "running",
		"agent_name": agentName,
	}, nil
}

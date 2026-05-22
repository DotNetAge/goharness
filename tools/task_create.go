package tools

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DotNetAge/goreact/core"
)

type TaskCreateTool struct {
	spawn   SpawnFunc
	counter atomic.Int64
}

func NewTaskCreateTool(spawn SpawnFunc) *TaskCreateTool {
	return &TaskCreateTool{spawn: spawn}
}

func (t *TaskCreateTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskCreate",
		Description: "Create a background task that runs an agent asynchronously. Returns immediately with a task_id. Use TaskGet to retrieve output or TaskList to see all tasks.",
		Prompt: `Create and start a background task. The task runs an agent asynchronously and returns immediately.

Use cases:
- Long-running agent tasks that you want to track
- Parallel agent execution — create multiple tasks and check their status later
- Tasks that need to be monitored or stopped later
- Dependent task chains — use depends_on to express ordering

The task_id returned can be used with:
- TaskGet: retrieve task output and status
- TaskList: see all tasks in the current session
- TaskUpdate: update task description, status, or metadata
- TaskStop: stop a running task

When depends_on is provided, the task stays in "pending" status until all listed
dependencies reach a terminal state (completed, failed, or stopped). TaskGet
and TaskList will report "blocked_by" indicating which dependencies are still
unresolved.

Usage:
- Provide a clear, descriptive task_description
- Specify the agent_name to run the task
- Optional: use depends_on to sequence task execution
- Multiple TaskCreate calls in the same round run in parallel`,
		Tags:    []string{"task", "create", "async", "agent", "orchestration"},
		IsAsync: true,
		Parameters: []core.Parameter{
			{Name: "task_description", Type: "string", Description: "Clear description of what the task should accomplish.", Required: true},
			{Name: "agent_name", Type: "string", Description: "Name of the agent to execute the task.", Required: true},
			{Name: "depends_on", Type: "string", Description: "Comma-separated list of task_ids that must resolve before this task starts.", Required: false},
		},
	}
}

func (t *TaskCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskDesc, _ := params["task_description"].(string)
	if taskDesc == "" {
		return nil, fmt.Errorf("task_description is required")
	}
	agentName, _ := params["agent_name"].(string)
	if agentName == "" {
		return nil, fmt.Errorf("agent_name is required")
	}

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.EmitEvent == nil {
		return nil, fmt.Errorf("TaskCreate requires ToolContext with EventBus")
	}
	if t.spawn == nil {
		return nil, fmt.Errorf("TaskCreate: SpawnFunc not configured")
	}
	if tc.SessionID == "" {
		return nil, fmt.Errorf("TaskCreate: SessionID not set")
	}

	taskID := fmt.Sprintf("task-%d", t.counter.Add(1))

	// Parse optional depends_on
	var dependsOn []string
	if deps, ok := params["depends_on"].(string); ok && deps != "" {
		for _, d := range strings.Split(deps, ",") {
			depID := strings.TrimSpace(d)
			if depID == "" {
				continue
			}
			depTask, err := GetTask(ctx, tc.SessionID, depID)
			if err != nil {
				return nil, fmt.Errorf("failed to verify dependency %q: %w", depID, err)
			}
			if depTask == nil {
				return nil, fmt.Errorf("dependency %q not found", depID)
			}
			dependsOn = append(dependsOn, depID)
		}
	}

	task := &Task{
		ID:          taskID,
		Type:        TaskTypeAgent,
		Description: taskDesc,
		Status:      TaskPending,
		DependsOn:   dependsOn,
		AgentName:   agentName,
		Prompt:      taskDesc,
	}

	if err := CreateTask(ctx, tc.SessionID, task); err != nil {
		return nil, fmt.Errorf("failed to create task record: %w", err)
	}

	tc.EmitEvent(core.ReactEvent{
		AgentID: "main",
		Type:    core.SubtaskSpawned,
		Data:    map[string]any{"task_id": taskID, "agent_name": agentName, "task": taskDesc},
	})

	go func() {
		// Use a local copy to avoid data race with the parent goroutine
		localTask := &Task{
			ID:          task.ID,
			Type:        task.Type,
			Description: task.Description,
			Status:      TaskRunning,
			DependsOn:   task.DependsOn,
			AgentName:   task.AgentName,
			Prompt:      task.Prompt,
		}

		now := time.Now()
		localTask.StartedAt = &now
		if err := UpdateTask(ctx, tc.SessionID, localTask); err != nil {
			// Log but continue
		}

		// If the task has dependencies, wait for them to resolve.
		// Poll every 500ms; give up after 30 minutes to avoid orphan goroutines.
		if len(task.DependsOn) > 0 {
			const pollInterval = 500 * time.Millisecond
			const maxWait = 30 * time.Minute
			deadline := time.Now().Add(maxWait)
			for time.Now().Before(deadline) {
				blockedBy, err := IsTaskBlocked(task, func(id string) (*Task, error) {
					return GetTask(ctx, tc.SessionID, id)
				})
				if err != nil {
					localTask.Status = TaskFailed
					localTask.Error = fmt.Sprintf("dependency check failed: %v", err)
					failedAt := time.Now()
					localTask.CompletedAt = &failedAt
					_ = UpdateTask(ctx, tc.SessionID, localTask)
					return
				}
				if len(blockedBy) == 0 {
					break // all dependencies resolved
				}
				time.Sleep(pollInterval)
			}
			if time.Now().After(deadline) {
				localTask.Status = TaskFailed
				localTask.Error = "timed out waiting for dependencies"
				completedAt := time.Now()
				localTask.CompletedAt = &completedAt
				_ = UpdateTask(ctx, tc.SessionID, localTask)
				tc.EmitEvent(core.ReactEvent{
					AgentID: agentName,
					Type:    core.SubtaskCompleted,
					Data:    map[string]any{"task_id": taskID, "success": false, "error": "dependency timeout"},
				})
				return
			}
		}

		result, err := t.spawn(ctx, agentName, taskDesc)
		completedAt := time.Now()
		localTask.CompletedAt = &completedAt
		if err != nil {
			localTask.Status = TaskFailed
			localTask.Error = err.Error()
		} else {
			localTask.Status = TaskCompleted
			localTask.Result = result
		}
		if updateErr := UpdateTask(ctx, tc.SessionID, localTask); updateErr != nil {
			// Log but continue
		}

		if tc.ResultStore != nil {
			taskResult := &core.TaskResult{
				TaskID: taskID,
				Result: result,
				Done:   true,
			}
			if err != nil {
				taskResult.Error = err.Error()
			}
			tc.ResultStore.Store(taskID, taskResult)
		}

		tc.EmitEvent(core.ReactEvent{
			AgentID: agentName,
			Type:    core.SubtaskCompleted,
			Data:    map[string]any{"task_id": taskID, "success": err == nil},
		})
	}()

	return map[string]any{
		"task_id":          taskID,
		"status":           "running",
		"agent_name":       agentName,
		"task_description": taskDesc,
	}, nil
}

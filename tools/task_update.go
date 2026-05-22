package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/DotNetAge/goreact/core"
)

type TaskUpdateTool struct{}

func NewTaskUpdateTool() *TaskUpdateTool {
	return &TaskUpdateTool{}
}

func (t *TaskUpdateTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskUpdate",
		Description: "Update a task's description and/or status. Status transitions follow a state machine: pending→running, pending→stopped, running→completed/failed/stopped.",
		Prompt: `Update a task's metadata (description) or advance its status.

Use cases:
- Refine or clarify the task description after further analysis
- Add context to a task that was created too generically
- Mark a pending task as "running" to start execution
- Mark a running/asynchronous task as "completed" or "failed" when done
- Stop a pending or running task

Status transitions (valid):
- pending  → running, stopped
- running  → completed, failed, stopped
- completed, failed, stopped are terminal (no further transitions)

When changing status, timestamps are managed automatically:
- pending → running: started_at is set
- → completed, failed, stopped: completed_at is set

Required parameter:
- task_id: the unique identifier of the task to update

Optional parameters:
- description: new description for the task
- status: new status for the task (pending, running, completed, failed, stopped)

At least one of description or status must be provided.`,
		Tags: []string{"task", "update", "metadata", "orchestration"},
		Parameters: []core.Parameter{
			{Name: "task_id", Type: "string", Description: "The unique identifier of the task to update.", Required: true},
			{Name: "description", Type: "string", Description: "New description for the task.", Required: false},
			{Name: "status", Type: "string", Description: "New status: pending, running, completed, failed, or stopped.", Required: false},
		},
	}
}

func (t *TaskUpdateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskID, _ := params["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskUpdate requires ToolContext with SessionID")
	}

	task, err := GetTask(ctx, tc.SessionID, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}

	updated := false

	// Update description
	if desc, ok := params["description"].(string); ok && desc != "" {
		task.Description = desc
		task.Prompt = desc
		updated = true
	}

	// Update status with transition validation
	if statusStr, ok := params["status"].(string); ok && statusStr != "" {
		newStatus := TaskStatus(statusStr)
		if !ValidTaskTransition(task.Status, newStatus) {
			return nil, fmt.Errorf("invalid status transition: %s → %s", task.Status, newStatus)
		}

		// Manage timestamps based on transition
		now := time.Now()
		switch {
		case task.Status == TaskPending && newStatus == TaskRunning:
			task.StartedAt = &now
		case newStatus == TaskCompleted || newStatus == TaskFailed || newStatus == TaskStopped:
			task.CompletedAt = &now
		}

		task.Status = newStatus
		updated = true
	}

	if !updated {
		return map[string]any{
			"success": false,
			"message": "No update parameters provided. Provide description, status, or both.",
		}, nil
	}

	if err := UpdateTask(ctx, tc.SessionID, task); err != nil {
		return nil, fmt.Errorf("failed to update task: %w", err)
	}

	return map[string]any{
		"success": true,
		"message": fmt.Sprintf("Task %q updated successfully", taskID),
		"task_id": taskID,
		"status":  string(task.Status),
	}, nil
}

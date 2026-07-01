package tools

import (
	"context"
	"fmt"
)

type TaskGetTool struct{}

func NewTaskGetTool() *TaskGetTool {
	return &TaskGetTool{}
}

func (t *TaskGetTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TaskGet",
		Description: "Get detailed information about a specific task.",
		Prompt: `Get detailed information about a specific task by task_id.

Use this to:
- Check the current status of a task (pending, in_progress, completed)
- See who owns a task
- Check what tasks block this one (blockedBy) or are blocked by it (blocks)
- Get the full description of what needs to be done

Required parameter:
- task_id: the unique identifier of the task (from TaskCreate or TaskList)

Returns:
- task_id, subject, description, status
- owner: who is responsible
- blocks: tasks that depend on this one (this must complete first)
- blocked_by: tasks that this one depends on (they must complete first)
- created_at: when the task was created`,
		Tags: []string{"task", "get", "status", "planning"},
		Parameters: []Parameter{
			{Name: "task_id", Type: "string", Description: "The unique identifier of the task to retrieve.", Required: true},
		},
	}
}

func (t *TaskGetTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskID, _ := params["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TaskGet requires ToolContext with SessionID")
	}

	task, err := GetTask(ctx, tc.Session.ID(), taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}

	result := map[string]any{
		"task_id":     task.ID,
		"subject":     task.Subject,
		"description": task.Description,
		"status":      string(task.Status),
		"created_at":  task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if task.ActiveForm != "" {
		result["active_form"] = task.ActiveForm
	}
	if len(task.Metadata) > 0 {
		result["metadata"] = task.Metadata
	}

	if task.Owner != "" {
		result["owner"] = task.Owner
	}
	if len(task.Blocks) > 0 {
		result["blocks"] = task.Blocks
	}
	if len(task.BlockedBy) > 0 {
		result["blocked_by"] = task.BlockedBy
	}

	return result, nil
}

package tools

import (
	"context"
	"fmt"
)

type TaskListTool struct{}

func NewTaskListTool() *TaskListTool {
	return &TaskListTool{}
}

func (t *TaskListTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TaskList",
		Description: "List all tasks in the current session with their status, owner, and dependency information.",
		Prompt: `List all tasks in the current session.

Use this to:
- See the full task list and current progress
- Find task IDs to use with TaskGet or TaskUpdate
- Identify blocked tasks and who they depend on
- See which tasks are assigned to whom

Optional filters:
- status_filter: only show tasks with this status (pending, in_progress, completed, cancelled)
- owner_filter: only show tasks assigned to this agent

Returns a list with:
- task_id: unique identifier for each task
- subject: short title
- status: pending, in_progress, completed, or cancelled
- owner: who is responsible (if assigned)
- blocked_by: tasks blocking this one (if any)`,
		Tags: []string{"task", "list", "status", "planning"},
		Parameters: []Parameter{
			{Name: "status_filter", Type: "string", Description: "Optional: filter by status (pending, in_progress, completed, cancelled).", Required: false},
			{Name: "owner_filter", Type: "string", Description: "Optional: filter by assigned owner.", Required: false},
		},
	}
}

func (t *TaskListTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskList requires ToolContext with SessionID")
	}

	statusFilter, _ := params["status_filter"].(string)
	ownerFilter, _ := params["owner_filter"].(string)

	taskIDs, err := ListTasks(ctx, tc.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(taskIDs) == 0 {
		return map[string]any{
			"tasks":   []any{},
			"message": "No tasks found in this session",
		}, nil
	}

	var tasks []map[string]any
	for _, id := range taskIDs {
		task, err := GetTask(ctx, tc.SessionID, id)
		if err != nil || task == nil {
			continue
		}

		if statusFilter != "" && string(task.Status) != statusFilter {
			continue
		}
		if ownerFilter != "" && task.Owner != ownerFilter {
			continue
		}

		taskInfo := map[string]any{
			"task_id":    task.ID,
			"subject":    task.Subject,
			"status":     string(task.Status),
			"created_at": task.CreatedAt.Format("2006-01-02 15:04:05"),
		}

		if task.Owner != "" {
			taskInfo["owner"] = task.Owner
		}
		if task.ActiveForm != "" {
			taskInfo["active_form"] = task.ActiveForm
		}
		if len(task.Metadata) > 0 {
			taskInfo["metadata"] = task.Metadata
		}
		if len(task.BlockedBy) > 0 {
			taskInfo["blocked_by"] = task.BlockedBy
		}

		tasks = append(tasks, taskInfo)
	}

	return map[string]any{
		"tasks": tasks,
		"count": len(tasks),
	}, nil
}

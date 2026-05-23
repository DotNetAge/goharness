package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goreact/core"
)

type TaskStopTool struct{}

func NewTaskStopTool() *TaskStopTool {
	return &TaskStopTool{}
}

func (t *TaskStopTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskStop",
		Description: "Abort/cancel a task. The task is permanently removed from the task list.",
		Prompt: `Abort a task that is no longer needed.

Use this to:
- Cancel a task that has been superseded
- Remove a task created by mistake
- Abort a task that is no longer relevant

Note: TaskStop permanently removes the task. Use TaskUpdate to change status
gracefully (e.g., mark as completed normally).

Required parameter:
- task_id: the unique identifier of the task to stop`,
		Tags: []string{"task", "stop", "cancel", "abort"},
		Parameters: []core.Parameter{
			{Name: "task_id", Type: "string", Description: "The unique identifier of the task to stop.", Required: true},
		},
	}
}

func (t *TaskStopTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskID, _ := params["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskStop requires ToolContext with SessionID")
	}

	task, err := GetTask(ctx, tc.SessionID, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}

	if err := DeleteTask(ctx, tc.SessionID, taskID); err != nil {
		return nil, fmt.Errorf("failed to stop task: %w", err)
	}

	return map[string]any{
		"success": true,
		"message": fmt.Sprintf("Task %q stopped and removed", taskID),
		"task_id": taskID,
	}, nil
}

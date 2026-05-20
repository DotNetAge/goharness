package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goreact/core"
)

type TaskGetTool struct{}

func NewTaskGetTool() *TaskGetTool {
	return &TaskGetTool{}
}

func (t *TaskGetTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskGet",
		MaxResultSizeChars: 50000,
		Description: "Get detailed information about a specific task including its status, output, and any errors.",
		Prompt: `Get detailed information about a specific task by task_id.

Use this to:
- Check the current status of a task (pending, running, completed, failed, stopped)
- Retrieve the task's result/output when it completes
- See error messages for failed tasks
- Monitor task progress

Required parameter:
- task_id: the unique identifier of the task (from TaskCreate or TaskList)

Returns:
- task_id, status, type, description
- result: the task's output (when completed)
- error: error message (when failed)
- created_at, started_at, completed_at timestamps
- agent_name: which agent ran the task`,
		Tags: []string{"task", "get", "status", "output", "orchestration"},
		Parameters: []core.Parameter{
			{Name: "task_id", Type: "string", Description: "The unique identifier of the task to retrieve.", Required: true},
		},
	}
}

func (t *TaskGetTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskID, _ := params["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskGet requires ToolContext with SessionID")
	}

	task, err := GetTask(ctx, tc.SessionID, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}

	result := map[string]any{
		"task_id":     task.ID,
		"status":      string(task.Status),
		"type":        string(task.Type),
		"description": task.Description,
		"created_at":  task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if task.AgentName != "" {
		result["agent_name"] = task.AgentName
	}
	if task.Prompt != "" {
		result["prompt"] = task.Prompt
	}
	if task.StartedAt != nil {
		result["started_at"] = task.StartedAt.Format("2006-01-02 15:04:05")
	}
	if task.CompletedAt != nil {
		result["completed_at"] = task.CompletedAt.Format("2006-01-02 15:04:05")
	}
	if task.Result != "" {
		result["result"] = task.Result
	}
	if task.Error != "" {
		result["error"] = task.Error
	}
	if task.OutputPath != "" {
		result["output_path"] = task.OutputPath
	}

	return result, nil
}

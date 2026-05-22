package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goreact/core"
)

type TaskListTool struct{}

func NewTaskListTool() *TaskListTool {
	return &TaskListTool{}
}

func (t *TaskListTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskList",
		Description: "List all tasks in the current session with their status. Returns task IDs, descriptions, types, and current status.",
		Prompt: `List all tasks in the current session.

Use this to:
- See all running, pending, completed, or failed tasks
- Find task IDs to use with TaskGet or TaskStop
- Monitor overall task progress

Returns a summary table with:
- task_id: unique identifier for each task
- status: pending, running, completed, failed, or stopped
- type: agent or shell
- description: what the task is doing
- agent_name: which agent is running the task (for agent tasks)`,
		Tags: []string{"task", "list", "status", "orchestration"},
		Parameters: []core.Parameter{},
	}
}

func (t *TaskListTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskList requires ToolContext with SessionID")
	}

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

		taskInfo := map[string]any{
			"task_id":     task.ID,
			"status":      string(task.Status),
			"type":        string(task.Type),
			"description": task.Description,
			"created_at":  task.CreatedAt.Format("2006-01-02 15:04:05"),
		}

		if len(task.DependsOn) > 0 {
			taskInfo["depends_on"] = task.DependsOn
			blockedBy, err := IsTaskBlocked(task, func(id string) (*Task, error) {
				return GetTask(ctx, tc.SessionID, id)
			})
			if err == nil && len(blockedBy) > 0 {
				taskInfo["blocked_by"] = blockedBy
			}
		}

		if task.AgentName != "" {
			taskInfo["agent_name"] = task.AgentName
		}
		if task.StartedAt != nil {
			taskInfo["started_at"] = task.StartedAt.Format("2006-01-02 15:04:05")
		}
		if task.CompletedAt != nil {
			taskInfo["completed_at"] = task.CompletedAt.Format("2006-01-02 15:04:05")
		}
		if task.Error != "" {
			taskInfo["error"] = task.Error
		}

		tasks = append(tasks, taskInfo)
	}

	return map[string]any{
		"tasks": tasks,
		"count": len(tasks),
	}, nil
}

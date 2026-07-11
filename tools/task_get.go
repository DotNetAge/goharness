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
		Description: "获取特定任务的详细信息。",
		Prompt: `通过 task_id 获取特定任务的详细信息。

用途：
- 检查任务的当前状态（pending、in_progress、completed）
- 查看谁拥有任务
- 检查哪些任务阻塞此任务（blockedBy）或被此任务阻塞（blocks）
- 获取需要完成内容的完整描述

必需参数：
- task_id：任务的唯一标识符（来自 TaskCreate 或 TaskList）

返回：
- task_id、subject、description、status
- owner：负责人
- blocks：依赖此任务的任务（此任务必须先完成）
- blocked_by：此任务依赖的任务（它们必须先完成）
- created_at：任务创建时间`,
		Tags: []string{"task", "get", "status", "planning"},
		Parameters: []Parameter{
			{Name: "task_id", Type: "string", Description: "要检索的任务的唯一标识符。", Required: true},
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
		return nil, fmt.Errorf("TaskGet 需要包含 SessionID 的 ToolContext")
	}

	task, err := GetTask(ctx, tc.Session.ID(), taskID)
	if err != nil {
		return nil, fmt.Errorf("获取任务失败：%w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("任务 %q 未找到", taskID)
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

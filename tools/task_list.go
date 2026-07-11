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
		Description: "列出当前会话中的所有任务及其状态、所有者和依赖信息。",
		Prompt: `列出当前会话中的所有任务。

用途：
- 查看完整的任务列表和当前进度
- 查找用于 TaskGet 或 TaskUpdate 的任务 ID
- 识别被阻塞的任务及其依赖关系
- 查看哪些任务分配给了谁

可选过滤器：
- status_filter：仅显示此状态的任务（pending、in_progress、completed、cancelled）
- owner_filter：仅显示分配给此代理的任务

返回列表包含：
- task_id：每个任务的唯一标识符
- subject：简短标题
- status：pending、in_progress、completed 或 cancelled
- owner：负责人（如果已分配）
- blocked_by：阻塞此任务的任务（如果有）`,
		Tags: []string{"task", "list", "status", "planning"},
		Parameters: []Parameter{
			{Name: "status_filter", Type: "string", Description: "可选：按状态过滤（pending、in_progress、completed、cancelled）。", Required: false},
			{Name: "owner_filter", Type: "string", Description: "可选：按分配的所有者过滤。", Required: false},
		},
	}
}

func (t *TaskListTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TaskList 需要包含 SessionID 的 ToolContext")
	}

	statusFilter, _ := params["status_filter"].(string)
	ownerFilter, _ := params["owner_filter"].(string)

	taskIDs, err := ListTasks(ctx, tc.Session.ID())
	if err != nil {
		return nil, fmt.Errorf("列出任务失败：%w", err)
	}

	if len(taskIDs) == 0 {
		return map[string]any{
			"tasks":   []any{},
			"message": "此会话中未找到任务",
		}, nil
	}

	var tasks []map[string]any
	for _, id := range taskIDs {
		task, err := GetTask(ctx, tc.Session.ID(), id)
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

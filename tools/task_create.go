package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type TaskCreateTool struct{}

func NewTaskCreateTool() *TaskCreateTool {
	return &TaskCreateTool{}
}

func (t *TaskCreateTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TaskCreate",
		Description: "在任务列表中创建任务以跟踪和规划工作。使用 TaskUpdate 标记进度，使用 TaskGet/TaskList 检查状态。",
		Prompt: `在任务列表中创建规划记录。这仅用于跟踪——请使用 SubAgent 或工具进行实际执行。

工作流程：先创建任务 → 逐个处理 → 完成后立即通过 TaskUpdate 标记为完成。

状态生命周期：pending → in_progress → completed（或 cancelled）。`,
		Tags:    []string{"task", "create", "planning", "tracking"},
		IsAsync: false,
		Parameters: []Parameter{
			{Name: "subject", Type: "string", Description: "任务的简短标题。", Required: true},
			{Name: "description", Type: "string", Description: "需要完成内容的详细描述。", Required: true},
			{Name: "active_form", Type: "string", Description: "执行期间显示的现在进行时形式（例如 '正在运行测试'）。", Required: false},
			{Name: "metadata", Type: "object", Description: "附加到任务的任意元数据键值对。", Required: false},
		},
	}
}

func (t *TaskCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	subject, _ := params["subject"].(string)
	if subject == "" {
		return nil, fmt.Errorf("subject 不能为空")
	}
	description, _ := params["description"].(string)
	if description == "" {
		return nil, fmt.Errorf("description 不能为空")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TaskCreate requires ToolContext with SessionID")
	}

	taskID := uuid.NewString()

	task := &Task{
		ID:          taskID,
		Subject:     subject,
		Description: description,
		Status:      TaskPending,
		CreatedAt:   time.Now(),
	}

	if activeForm, ok := params["active_form"].(string); ok && activeForm != "" {
		task.ActiveForm = activeForm
	}
	if meta, ok := params["metadata"].(map[string]any); ok && len(meta) > 0 {
		task.Metadata = meta
	}

	if err := CreateTask(ctx, tc.Session.ID(), task); err != nil {
		return nil, fmt.Errorf("创建任务失败：%w", err)
	}

	return map[string]any{
		"task_id":     taskID,
		"status":      string(TaskPending),
		"subject":     subject,
		"description": description,
		"active_form": task.ActiveForm,
		"metadata":    task.Metadata,
	}, nil
}

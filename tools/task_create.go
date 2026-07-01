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
		Description: "Create a task in the task list for tracking and planning work. Use TaskUpdate to mark progress, TaskGet/TaskList to check status.",
		Prompt: `Create a planning record in the task list. This is for TRACKING only — use SubAgent or tools for actual execution.

Workflow: create tasks first → work through them one by one → mark complete via TaskUpdate immediately when done.

Status lifecycle: pending → in_progress → completed (or cancelled).`,
		Tags:    []string{"task", "create", "planning", "tracking"},
		IsAsync: false,
		Parameters: []Parameter{
			{Name: "subject", Type: "string", Description: "Short title for the task.", Required: true},
			{Name: "description", Type: "string", Description: "Detailed description of what needs to be done.", Required: true},
			{Name: "active_form", Type: "string", Description: "Present continuous form shown during execution (e.g. 'Running tests').", Required: false},
			{Name: "metadata", Type: "object", Description: "Arbitrary metadata key-value pairs to attach to the task.", Required: false},
		},
	}
}

func (t *TaskCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	subject, _ := params["subject"].(string)
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	description, _ := params["description"].(string)
	if description == "" {
		return nil, fmt.Errorf("description is required")
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
		return nil, fmt.Errorf("failed to create task: %w", err)
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

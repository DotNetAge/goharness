package tools

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DotNetAge/goreact/core"
)

type TaskCreateTool struct {
	counter atomic.Int64
}

func NewTaskCreateTool() *TaskCreateTool {
	return &TaskCreateTool{}
}

func (t *TaskCreateTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TaskCreate",
		Description: "Create a task in the task list for tracking and planning work. Use TaskUpdate to mark progress, TaskGet/TaskList to check status.",
		Prompt: `Create a task in the task list to break down complex work into manageable steps.

Use cases:
- Break down a complex request into smaller, trackable sub-tasks
- Plan your approach before executing: create tasks first, then work through them
- Track progress across multiple steps of a workflow
- Coordinate with team members by assigning tasks via owner field

Each task has:
- subject: short title (like a headline)
- description: detailed description of what needs to be done
- status: pending → in_progress → completed (update via TaskUpdate)
- owner: who is responsible (set via TaskUpdate)
- blocks / blockedBy: express dependencies between tasks

This tool only creates the planning record. Use SubAgent/Agent tools for actual execution.

Usage:
- Create tasks with clear subject and description
- After creating all tasks, work through them one by one
- Use TaskUpdate(status="in_progress") when starting work on a task
- Use TaskUpdate(status="completed") when finished
- Use blocks/blockedBy (via TaskUpdate) to express task ordering`,
		Tags:    []string{"task", "create", "planning", "tracking"},
		IsAsync: false,
		Parameters: []core.Parameter{
			{Name: "subject", Type: "string", Description: "Short title for the task.", Required: true},
			{Name: "description", Type: "string", Description: "Detailed description of what needs to be done.", Required: true},
			{Name: "activeForm", Type: "string", Description: "Present continuous form shown during execution (e.g. 'Running tests').", Required: false},
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

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskCreate requires ToolContext with SessionID")
	}

	taskID := fmt.Sprintf("task-%d", t.counter.Add(1))

	task := &Task{
		ID:          taskID,
		Subject:     subject,
		Description: description,
		Status:      TaskPending,
		CreatedAt:   time.Now(),
	}

	if activeForm, ok := params["activeForm"].(string); ok && activeForm != "" {
		task.ActiveForm = activeForm
	}
	if meta, ok := params["metadata"].(map[string]any); ok && len(meta) > 0 {
		task.Metadata = meta
	}

	if err := CreateTask(ctx, tc.SessionID, task); err != nil {
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

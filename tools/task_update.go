package tools

import (
	"context"
	"fmt"
	"slices"
)

type TaskUpdateTool struct{}

func NewTaskUpdateTool() *TaskUpdateTool {
	return &TaskUpdateTool{}
}

func (t *TaskUpdateTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TaskUpdate",
		Description: "Update a task's subject, description, status, owner, or dependency relationships.",
		Prompt: `Update a task's metadata or advance it through its lifecycle.

Use cases:
- Update subject or description to clarify what needs to be done
- Mark a task as in_progress when starting work
- Mark a task as completed when finished
- Cancel a task that is no longer needed
- Assign a task to yourself or a teammate via owner
- Express dependencies between tasks with blocks/blockedBy

Status transitions:
- pending → in_progress (start working)
- pending → completed (skip)
- pending → cancelled (abandon)
- in_progress → completed (finish)
- in_progress → cancelled (abandon)

Dependencies:
- Use addBlocks to say "this task blocks the listed tasks"
- Use addBlockedBy to say "this task is blocked by the listed tasks"
- Example: Task A blocks Task B → B can't start until A is completed
- Cycle detection is automatic: if A depends on B and B already depends on A, the update is rejected

At least one of subject, description, status, owner, addBlocks, or addBlockedBy must be provided.`,
		Tags: []string{"task", "update", "status", "planning"},
		Parameters: []Parameter{
			{Name: "task_id", Type: "string", Description: "The ID of the task to update.", Required: true},
			{Name: "subject", Type: "string", Description: "New subject (short title) for the task.", Required: false},
			{Name: "description", Type: "string", Description: "New detailed description of what needs to be done.", Required: false},
			{Name: "status", Type: "string", Description: "New status: pending, in_progress, completed, or cancelled.", Required: false},
			{Name: "owner", Type: "string", Description: "Assign the task to an agent (by name).", Required: false},
			{Name: "addBlocks", Type: "array", Description: "Task IDs that this task now blocks (depend on this one).", Required: false},
			{Name: "addBlockedBy", Type: "array", Description: "Task IDs that this task is now blocked by (depends on them).", Required: false},
			{Name: "active_form", Type: "string", Description: "Present continuous form shown during execution (e.g. 'Running tests').", Required: false},
			{Name: "metadata", Type: "object", Description: "Arbitrary metadata to merge into the task. Set a key to null to delete it.", Required: false},
		},
	}
}

func (t *TaskUpdateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	taskID, _ := params["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TaskUpdate requires ToolContext with SessionID")
	}

	task, err := GetTask(ctx, tc.SessionID, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task %q not found", taskID)
	}

	updated := false

	// Update basic fields
	if subj, ok := params["subject"].(string); ok && subj != "" && subj != task.Subject {
		task.Subject = subj
		updated = true
	}
	if desc, ok := params["description"].(string); ok && desc != "" && desc != task.Description {
		task.Description = desc
		updated = true
	}
	if owner, ok := params["owner"].(string); ok && owner != "" && owner != task.Owner {
		task.Owner = owner
		updated = true
	}

	// Update active_form
	if activeForm, ok := params["active_form"].(string); ok && activeForm != "" && activeForm != task.ActiveForm {
		task.ActiveForm = activeForm
		updated = true
	}

	// Update metadata (merge)
	if meta, ok := params["metadata"].(map[string]any); ok {
		if task.Metadata == nil {
			task.Metadata = make(map[string]any)
		}
		for k, v := range meta {
			if v == nil {
				delete(task.Metadata, k)
			} else {
				task.Metadata[k] = v
			}
		}
		updated = true
	}

	// Update status with transition validation
	if statusStr, ok := params["status"].(string); ok && statusStr != "" {
		newStatus := TaskStatus(statusStr)
		if newStatus != task.Status {
			if !ValidTaskTransition(task.Status, newStatus) {
				return nil, fmt.Errorf("invalid status transition: %s → %s", task.Status, newStatus)
			}
			task.Status = newStatus
			updated = true
		}
	}

	// Add blocks (this task blocks listed tasks → listed tasks' blockedBy += this)
	if rawBlocks, ok := params["addBlocks"].([]any); ok && len(rawBlocks) > 0 {
		for _, raw := range rawBlocks {
			if blockID, ok := raw.(string); ok && blockID != "" {
				if canReach(ctx, tc.SessionID, blockID, taskID) {
					return nil, fmt.Errorf("adding block %q would create a circular dependency", blockID)
				}
				if !slices.Contains(task.Blocks, blockID) {
					task.Blocks = append(task.Blocks, blockID)
				}
				blockedTask, err := GetTask(ctx, tc.SessionID, blockID)
				if err == nil && blockedTask != nil {
					if !slices.Contains(blockedTask.BlockedBy, taskID) {
						blockedTask.BlockedBy = append(blockedTask.BlockedBy, taskID)
						if err := UpdateTask(ctx, tc.SessionID, blockedTask); err != nil {
							getLogger(ctx).Warn("failed to update inverse dependency",
								"task_id", blockID, "error", err)
						}
					}
				}
			}
		}
		updated = true
	}

	// Add blockedBy (this task is blocked by listed tasks → listed tasks' blocks += this)
	if rawBlockedBy, ok := params["addBlockedBy"].([]any); ok && len(rawBlockedBy) > 0 {
		for _, raw := range rawBlockedBy {
			if depID, ok := raw.(string); ok && depID != "" {
				if canReach(ctx, tc.SessionID, taskID, depID) {
					return nil, fmt.Errorf("adding blockedBy %q would create a circular dependency", depID)
				}
				if !slices.Contains(task.BlockedBy, depID) {
					task.BlockedBy = append(task.BlockedBy, depID)
				}
				blockingTask, err := GetTask(ctx, tc.SessionID, depID)
				if err == nil && blockingTask != nil {
					if !slices.Contains(blockingTask.Blocks, taskID) {
						blockingTask.Blocks = append(blockingTask.Blocks, taskID)
						if err := UpdateTask(ctx, tc.SessionID, blockingTask); err != nil {
							getLogger(ctx).Warn("failed to update inverse dependency",
								"task_id", depID, "error", err)
						}
					}
				}
			}
		}
		updated = true
	}

	if !updated {
		return map[string]any{
			"success": false,
			"message": "No changes provided. Provide at least one of: subject, description, status, owner, active_form, metadata, addBlocks, addBlockedBy.",
		}, nil
	}

	if err := UpdateTask(ctx, tc.SessionID, task); err != nil {
		return nil, fmt.Errorf("failed to update task: %w", err)
	}

	result := map[string]any{
		"success": true,
		"message": fmt.Sprintf("Task %q updated", taskID),
		"task_id": taskID,
		"status":  string(task.Status),
	}

	// Verification nudge: when completing tasks, suggest verifying after every 3 completions
	if task.Status == TaskCompleted {
		completedCount := 0
		allIDs, _ := ListTasks(ctx, tc.SessionID)
		for _, id := range allIDs {
			t, _ := GetTask(ctx, tc.SessionID, id)
			if t != nil && t.Status == TaskCompleted {
				completedCount++
			}
		}
		if completedCount > 0 && completedCount%3 == 0 {
			result["nudge"] = fmt.Sprintf("You have completed %d tasks. Consider verifying your work (e.g., review files, run tests) before proceeding to the next task.", completedCount)
		}
	}

	return result, nil
}

func canReach(ctx context.Context, sessionID, from, to string) bool {
	visited := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == to {
			return true
		}
		if visited[current] {
			continue
		}
		visited[current] = true
		t, _ := GetTask(ctx, sessionID, current)
		if t == nil {
			continue
		}
		for _, id := range t.Blocks {
			stack = append(stack, id)
		}
	}
	return false
}

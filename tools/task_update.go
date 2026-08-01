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
		Description: "推进任务的生命周期或更新其元数据。强制执行有效的状态转换并自动检测循环依赖。",
		Prompt: `更新任务的状态或元数据。至少必须更改一个字段。

有效的状态转换：
- pending → in_progress | completed | cancelled
- in_progress → completed | cancelled

依赖关系：使用 addBlocks/addBlockedBy 表示任务顺序。循环依赖会被自动拒绝（例如，A 阻塞 B 且 B 阻塞 A）。`,
		Tags: []string{"task", "update", "status", "planning"},
		Parameters: []Parameter{
			{Name: "task_id", Type: "string", Description: "要更新的任务的 ID。", Required: true},
			{Name: "subject", Type: "string", Description: "任务的新主题（简短标题）。", Required: false},
			{Name: "description", Type: "string", Description: "需要完成内容的新详细描述。", Required: false},
			{Name: "status", Type: "string", Description: "新状态：pending、in_progress、completed 或 cancelled。", Required: false},
			{Name: "owner", Type: "string", Description: "将任务分配给代理（按名称）。", Required: false},
			{Name: "addBlocks", Type: "array", Description: "此任务现在阻塞的任务 ID（依赖于本任务）。", Required: false},
			{Name: "addBlockedBy", Type: "array", Description: "此任务现在被其阻塞的任务 ID（本任务依赖于它们）。", Required: false},
			{Name: "active_form", Type: "string", Description: "执行期间显示的现在进行时形式（例如 '正在运行测试'）。", Required: false},
			{Name: "metadata", Type: "object", Description: "合并到任务中的任意元数据。将键设置为 null 以删除它。", Required: false},
		},
	}
}

func (t *TaskUpdateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawTaskID, _ := GetParam(params, "task_id")
	taskID, _ := rawTaskID.(string)
	if taskID == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("TaskUpdate", "task_id"))
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("%s", GuideMissingContext("TaskUpdate", "包含 SessionID 的 ToolContext"))
	}

	task, err := GetTask(ctx, tc.Session.ID(), taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("%s", GuideNotFound("任务", taskID, "使用 TaskCreate 创建该任务，或用 TaskList 查看现有任务 ID 后重试"))
	}

	updated := false

	// Update basic fields
	rawSubj, _ := GetParam(params, "subject")
	if subj, ok := rawSubj.(string); ok && subj != "" && subj != task.Subject {
		task.Subject = subj
		updated = true
	}
	rawDesc, _ := GetParam(params, "description")
	if desc, ok := rawDesc.(string); ok && desc != "" && desc != task.Description {
		task.Description = desc
		updated = true
	}
	rawOwner, _ := GetParam(params, "owner")
	if owner, ok := rawOwner.(string); ok && owner != "" && owner != task.Owner {
		task.Owner = owner
		updated = true
	}

	// Update active_form
	rawActiveForm, _ := GetParam(params, "active_form")
	if activeForm, ok := rawActiveForm.(string); ok && activeForm != "" && activeForm != task.ActiveForm {
		task.ActiveForm = activeForm
		updated = true
	}

	// Update metadata (merge)
	rawMeta, _ := GetParam(params, "metadata")
	if meta, ok := rawMeta.(map[string]any); ok {
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
	rawStatus, _ := GetParam(params, "status")
	if statusStr, ok := rawStatus.(string); ok && statusStr != "" {
		newStatus := TaskStatus(statusStr)
		if newStatus != task.Status {
			if !ValidTaskTransition(task.Status, newStatus) {
				return nil, fmt.Errorf("%s", GuideInvalidValue("TaskUpdate", "status", statusStr, "检查任务当前状态，使用合法的状态流转（pending → in_progress → completed / cancelled）后重试"))
			}
			task.Status = newStatus
			updated = true
		}
	}

	// Add blocks (this task blocks listed tasks → listed tasks' blockedBy += this)
	rawBlocksVal, _ := GetParam(params, "add_blocks")
	if rawBlocks, ok := rawBlocksVal.([]any); ok && len(rawBlocks) > 0 {
		for _, raw := range rawBlocks {
			if blockID, ok := raw.(string); ok && blockID != "" {
				if canReach(ctx, tc.Session.ID(), blockID, taskID) {
					return nil, fmt.Errorf("%s", BuildGuide(
						fmt.Sprintf("尝试为任务 %q 添加阻塞任务 %q", taskID, blockID),
						fmt.Sprintf("该操作会形成循环依赖：%q 已直接或间接依赖 %q，任务将永远无法完成", blockID, taskID),
						"不要添加会形成循环的依赖关系；检查现有 blocks/blocked_by，调整依赖方向后重试",
					))
				}
				if !slices.Contains(task.Blocks, blockID) {
					task.Blocks = append(task.Blocks, blockID)
				}
				blockedTask, err := GetTask(ctx, tc.Session.ID(), blockID)
				if err == nil && blockedTask != nil {
					if !slices.Contains(blockedTask.BlockedBy, taskID) {
						blockedTask.BlockedBy = append(blockedTask.BlockedBy, taskID)
						if err := UpdateTask(ctx, tc.Session.ID(), blockedTask); err != nil {
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
	rawBlockedByVal, _ := GetParam(params, "add_blocked_by")
	if rawBlockedBy, ok := rawBlockedByVal.([]any); ok && len(rawBlockedBy) > 0 {
		for _, raw := range rawBlockedBy {
			if depID, ok := raw.(string); ok && depID != "" {
				if canReach(ctx, tc.Session.ID(), taskID, depID) {
					return nil, fmt.Errorf("%s", BuildGuide(
						fmt.Sprintf("尝试为任务 %q 添加被阻塞任务 %q", taskID, depID),
						fmt.Sprintf("该操作会形成循环依赖：%q 已直接或间接阻塞 %q，任务将永远无法完成", depID, taskID),
						"不要添加会形成循环的依赖关系；检查现有 blocks/blocked_by，调整依赖方向后重试",
					))
				}
				if !slices.Contains(task.BlockedBy, depID) {
					task.BlockedBy = append(task.BlockedBy, depID)
				}
				blockingTask, err := GetTask(ctx, tc.Session.ID(), depID)
				if err == nil && blockingTask != nil {
					if !slices.Contains(blockingTask.Blocks, taskID) {
						blockingTask.Blocks = append(blockingTask.Blocks, taskID)
						if err := UpdateTask(ctx, tc.Session.ID(), blockingTask); err != nil {
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
			"message": "未提供任何更改。请至少提供以下之一：subject、description、status、owner、active_form、metadata、addBlocks、addBlockedBy。",
		}, nil
	}

	if err := UpdateTask(ctx, tc.Session.ID(), task); err != nil {
		return nil, err
	}

	result := map[string]any{
		"success": true,
		"message": fmt.Sprintf("任务 %q 已更新", taskID),
		"task_id": taskID,
		"status":  string(task.Status),
	}

	// Verification nudge: when completing tasks, suggest verifying after every 3 completions
	if task.Status == TaskCompleted {
		completedCount := 0
		allIDs, _ := ListTasks(ctx, tc.Session.ID())
		for _, id := range allIDs {
			t, _ := GetTask(ctx, tc.Session.ID(), id)
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

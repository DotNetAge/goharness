package tools

import (
	"context"
	"fmt"
)

type TeamGetTasksTool struct{}

func NewTeamGetTasksTool() *TeamGetTasksTool {
	return &TeamGetTasksTool{}
}

func (t *TeamGetTasksTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TeamGetTasks",
		Description: "获取分配给团队的所有任务，包括其状态、所有者和依赖关系。",
		Prompt: `检索属于特定团队的所有任务。

用途：
- 查看每个团队成员正在做什么
- 检查团队交付物的进度
- 识别团队内被阻塞的任务`,
		Tags: []string{"team", "tasks", "list", "status"},
		Parameters: []Parameter{
			{Name: "team_name", Type: "string", Description: "要获取任务的团队名称（来自 TeamCreate）。", Required: true},
		},
	}
}

func (t *TeamGetTasksTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	teamName, _ := params["team_name"].(string)
	if teamName == "" {
		return nil, fmt.Errorf("team_name 不能为空")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TeamGetTasks 需要包含 SessionID 的 ToolContext")
	}

	team, err := GetTeam(ctx, tc.Session.ID(), teamName)
	if err != nil {
		return nil, fmt.Errorf("获取团队失败：%w", err)
	}
	if team == nil {
		return nil, fmt.Errorf("团队 %q 未找到", teamName)
	}

	var tasks []map[string]any
	for _, taskID := range team.TaskIDs {
		task, err := GetTask(ctx, tc.Session.ID(), taskID)
		if err != nil || task == nil {
			continue
		}
		taskInfo := map[string]any{
			"task_id":    task.ID,
			"subject":    task.Subject,
			"status":     string(task.Status),
			"owner":      task.Owner,
			"created_at": task.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		if task.ActiveForm != "" {
			taskInfo["active_form"] = task.ActiveForm
		}
		if len(task.BlockedBy) > 0 {
			taskInfo["blocked_by"] = task.BlockedBy
		}
		tasks = append(tasks, taskInfo)
	}

	result := map[string]any{
		"team_name": teamName,
		"tasks":     tasks,
		"count":     len(tasks),
	}

	if len(tasks) == 0 {
		result["message"] = "此团队未分配任何任务"
	}

	return result, nil
}

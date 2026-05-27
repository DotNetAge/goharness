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
		Description: "Get all tasks assigned to a team, with their status, owner, and dependencies.",
		Prompt: `Retrieve all tasks belonging to a specific team.

Use this to:
- See what each team member is working on
- Check progress of team deliverables
- Identify blocked tasks within the team

Required parameter:
- team_name: the name of the team (from TeamCreate)`,
		Tags: []string{"team", "tasks", "list", "status"},
		Parameters: []Parameter{
			{Name: "team_name", Type: "string", Description: "Name of the team to get tasks for.", Required: true},
		},
	}
}

func (t *TeamGetTasksTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	teamName, _ := params["team_name"].(string)
	if teamName == "" {
		return nil, fmt.Errorf("team_name is required")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TeamGetTasks requires ToolContext with SessionID")
	}

	team, err := GetTeam(ctx, tc.SessionID, teamName)
	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}
	if team == nil {
		return nil, fmt.Errorf("team %q not found", teamName)
	}

	var tasks []map[string]any
	for _, taskID := range team.TaskIDs {
		task, err := GetTask(ctx, tc.SessionID, taskID)
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
		result["message"] = "No tasks assigned to this team"
	}

	return result, nil
}

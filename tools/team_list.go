package tools

import (
	"context"
	"fmt"
)

type TeamListTool struct{}

func NewTeamListTool() *TeamListTool {
	return &TeamListTool{}
}

func (t *TeamListTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TeamList",
		Description: "列出当前会话中的所有团队及其成员和状态。",
		Prompt: `列出当前会话中的所有团队。

用途：
- 查看所有活跃团队
- 查找用于其他团队操作的团队名称
- 监控团队组成和任务分配`,
		Tags:       []string{"team", "list", "status", "orchestration"},
		Parameters: []Parameter{},
	}
}

func (t *TeamListTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("%s", GuideMissingContext("TeamList", "包含 SessionID 的 ToolContext"))
	}

	teamNames, err := ListTeams(ctx, tc.Session.ID())
	if err != nil {
		return nil, err
	}

	if len(teamNames) == 0 {
		return map[string]any{
			"teams":   []any{},
			"message": "此会话中未找到团队",
		}, nil
	}

	var teams []map[string]any
	for _, name := range teamNames {
		team, err := GetTeam(ctx, tc.Session.ID(), name)
		if err != nil || team == nil {
			continue
		}

		teamInfo := map[string]any{
			"team_name":  team.Name,
			"leader":     team.Leader,
			"members":    team.Members,
			"status":     team.Status,
			"created_at": team.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		if len(team.TaskIDs) > 0 {
			teamInfo["task_ids"] = team.TaskIDs
		}
		if team.Description != "" {
			teamInfo["description"] = team.Description
		}

		teams = append(teams, teamInfo)
	}

	return map[string]any{
		"teams": teams,
		"count": len(teams),
	}, nil
}

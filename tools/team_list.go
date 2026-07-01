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
		Description: "List all teams in the current session with their members and status.",
		Prompt: `List all teams in the current session.

Use this to:
- See all active teams
- Find team names to use with other team operations
- Monitor team composition and task assignments

Returns:
- team_name: unique identifier for the team
- leader: the team leader agent
- members: list of team member agents
- task_ids: tasks dispatched to the team
- status: active or completed
- created_at: when the team was created`,
		Tags:       []string{"team", "list", "status", "orchestration"},
		Parameters: []Parameter{},
	}
}

func (t *TeamListTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TeamList requires ToolContext with SessionID")
	}

	teamNames, err := ListTeams(ctx, tc.Session.ID())
	if err != nil {
		return nil, fmt.Errorf("failed to list teams: %w", err)
	}

	if len(teamNames) == 0 {
		return map[string]any{
			"teams":   []any{},
			"message": "No teams found in this session",
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

package tools

import (
	"context"
	"fmt"
)

type TeamDeleteTool struct{}

func NewTeamDeleteTool() *TeamDeleteTool {
	return &TeamDeleteTool{}
}

func (t *TeamDeleteTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TeamDelete",
		Description: "Delete a team and clean up its associated data. All team members must be idle before deletion.",
		Prompt: `Delete a team and clean up its data.

Use this to:
- Clean up after a team has completed its work
- Remove a team that is no longer needed

Before deleting:
- All team members should have completed their tasks
- Use TeamList to verify team status

Required parameter:
- team_name: the name of the team to delete

Returns:
- success: whether the team was deleted
- message: status message`,
		Tags: []string{"team", "delete", "cleanup", "orchestration"},
		Parameters: []Parameter{
			{Name: "team_name", Type: "string", Description: "The name of the team to delete.", Required: true},
		},
	}
}

func (t *TeamDeleteTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	teamName, _ := params["team_name"].(string)
	if teamName == "" {
		return nil, fmt.Errorf("team_name is required")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TeamDelete requires ToolContext with SessionID")
	}

	team, err := GetTeam(ctx, tc.SessionID, teamName)
	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}
	if team == nil {
		return nil, fmt.Errorf("team %q not found", teamName)
	}

	if err := DeleteTeam(ctx, tc.SessionID, teamName); err != nil {
		return nil, fmt.Errorf("failed to delete team: %w", err)
	}

	return map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("Team %q deleted successfully", teamName),
		"team_name": teamName,
	}, nil
}

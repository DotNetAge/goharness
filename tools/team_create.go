package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/DotNetAge/goreact/core"
)

type TeamCreateTool struct {
	spawn SpawnFunc
}

func NewTeamCreateTool(spawn SpawnFunc) *TeamCreateTool {
	return &TeamCreateTool{spawn: spawn}
}

func (t *TeamCreateTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "TeamCreate",
		Description: "Create a team of agents that work together on a complex task. The team leader coordinates work and delegates to team members.",
		Prompt: `Create a team of agents to collaboratively solve a complex task.

A team consists of:
- A team leader (you, or a designated agent) who coordinates the work
- Team members (other agents) who execute specific parts of the task

Use this when:
- The task is too complex for a single agent
- Multiple specialized agents need to collaborate
- You want to organize parallel work streams with coordination

The team creation is immediate. After creating the team, use TaskCreate to
create planning entries and Delegate/Agent tools to dispatch work to members.

Required parameters:
- team_name: short, unique name for the team (kebab-case, e.g. "data-analysis-team")
- description: what the team is working on
- leader: the name of the team leader agent (usually yourself)
- members: array of agent names who will be team members

Optional parameters:
- tasks: array of task descriptions to create planning entries for each member

Returns:
- team_name, leader, members list
- task_ids if tasks were created`,
		Tags: []string{"team", "create", "swarm", "orchestration", "collaboration"},
		Parameters: []core.Parameter{
			{Name: "team_name", Type: "string", Description: "Short, unique name for the team (kebab-case).", Required: true},
			{Name: "description", Type: "string", Description: "What the team is working on.", Required: true},
			{Name: "leader", Type: "string", Description: "Name of the team leader agent.", Required: true},
			{Name: "members", Type: "array", Description: "Array of agent names who will be team members.", Required: true},
			{Name: "tasks", Type: "array", Description: "Array of task descriptions to create planning entries for team members.", Required: false},
		},
	}
}

func (t *TeamCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	teamName, _ := params["team_name"].(string)
	if teamName == "" {
		return nil, fmt.Errorf("team_name is required")
	}
	description, _ := params["description"].(string)
	if description == "" {
		return nil, fmt.Errorf("description is required")
	}
	leader, _ := params["leader"].(string)
	if leader == "" {
		return nil, fmt.Errorf("leader is required")
	}

	var members []string
	if rawMembers, ok := params["members"].([]any); ok {
		for _, m := range rawMembers {
			if str, ok := m.(string); ok && str != "" {
				members = append(members, str)
			}
		}
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("members is required and must contain at least one agent")
	}

	tc := core.GetToolContext(ctx)
	if tc == nil || tc.SessionID == "" {
		return nil, fmt.Errorf("TeamCreate requires ToolContext with SessionID")
	}

	team := &Team{
		Name:        teamName,
		Description: description,
		Leader:      leader,
		Members:     members,
	}

	if err := CreateTeam(ctx, tc.SessionID, team); err != nil {
		return nil, fmt.Errorf("failed to create team: %w", err)
	}

	var taskIDs []string
	if rawTasks, ok := params["tasks"].([]any); ok {
		for _, rawTask := range rawTasks {
			taskDesc, _ := rawTask.(string)
			if taskDesc == "" {
				continue
			}

			memberIdx := len(taskIDs) % len(members)
			memberName := members[memberIdx]
			taskID := fmt.Sprintf("team-%s-task-%d", teamName, len(taskIDs)+1)

			task := &Task{
				ID:          taskID,
				Subject:     taskDesc,
				Description: taskDesc,
				Status:      TaskPending,
				Owner:       memberName,
				CreatedAt:   time.Now(),
			}

			if err := CreateTask(ctx, tc.SessionID, task); err != nil {
				continue
			}
			taskIDs = append(taskIDs, taskID)
		}
	}

	result := map[string]any{
		"team_name": teamName,
		"leader":    leader,
		"members":   members,
		"message":   fmt.Sprintf("Team %q created with %d members", teamName, len(members)),
	}

	if len(taskIDs) > 0 {
		result["task_ids"] = taskIDs
		result["message"] = fmt.Sprintf("Team %q created with %d members and %d tasks created", teamName, len(members), len(taskIDs))
	}

	return result, nil
}

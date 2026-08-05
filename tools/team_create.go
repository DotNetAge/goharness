package tools

import (
	"context"
	"fmt"
	"time"
)

type TeamCreateTool struct {
	spawn SpawnFunc
}

func NewTeamCreateTool(spawn SpawnFunc) *TeamCreateTool {
	return &TeamCreateTool{spawn: spawn}
}

func (t *TeamCreateTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "TeamCreate",
		Description: "创建一个代理团队（仅登记团队名称、负责人与成员，不运行任何子代理）。创建后需由你自行使用 SubAgent 将任务分派给成员。",
		Prompt: `创建一个代理团队，仅登记团队定义（team_name、leader、members 及可选的任务规划条目）。

重要：此工具不会启动或运行任何子代理，也不会自动分派工作。团队成员不会自动开始执行任务。

团队包括：
- 团队负责人（你或指定的代理）负责协调工作
- 团队成员（其他代理）执行任务的特定部分

使用场景：
- 任务对单个代理来说太复杂
- 需要多个专业代理协作
- 你想组织有协调的并行工作流`,
		Tags: []string{"team", "create", "swarm", "orchestration", "collaboration"},
		Parameters: []Parameter{
			{Name: "team_name", Type: "string", Description: "团队的简短唯一名称（kebab-case）。", Required: true},
			{Name: "description", Type: "string", Description: "团队正在做什么。", Required: true},
			{Name: "leader", Type: "string", Description: "团队负责人代理的名称。", Required: true},
			{Name: "members", Type: "array", Description: "将成为团队成员的代理名称数组。", Required: true},
			{Name: "tasks", Type: "array", Description: "为团队成员创建规划条目的任务描述数组。", Required: false},
		},
	}
}

func (t *TeamCreateTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawTeamName, _ := GetParam(params, "team_name")
	teamName, _ := rawTeamName.(string)
	if teamName == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("TeamCreate", "team_name"))
	}
	rawDescription, _ := GetParam(params, "description")
	description, _ := rawDescription.(string)
	if description == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("TeamCreate", "description"))
	}
	rawLeader, _ := GetParam(params, "leader")
	leader, _ := rawLeader.(string)
	if leader == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("TeamCreate", "leader"))
	}

	var members []string
	rawMembersVal, _ := GetParam(params, "members")
	if rawMembers, ok := rawMembersVal.([]any); ok {
		for _, m := range rawMembers {
			if str, ok := m.(string); ok && str != "" {
				members = append(members, str)
			}
		}
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("%s", GuideInvalidValue("TeamCreate", "members", rawMembersVal, "在 members 数组中至少传入一个代理名称，可先使用 Ls/TeamList 等确认可用代理"))
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("%s", GuideMissingContext("TeamCreate", "包含 SessionID 的 ToolContext"))
	}

	team := &Team{
		Name:        teamName,
		Description: description,
		Leader:      leader,
		Members:     members,
	}

	if err := CreateTeam(ctx, tc.Session.ID(), team); err != nil {
		return nil, err
	}

	var taskIDs []string
	rawTasksVal, _ := GetParam(params, "tasks")
	if rawTasks, ok := rawTasksVal.([]any); ok {
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

			if err := CreateTask(ctx, tc.Session.ID(), task); err != nil {
				continue
			}
			taskIDs = append(taskIDs, taskID)
		}
	}

	result := map[string]any{
		"team_name": teamName,
		"leader":    leader,
		"members":   members,
		"message":   fmt.Sprintf("团队 %q 已创建，包含 %d 个成员。下一步：用 TaskCreate 为成员创建规划条目，再用 SubAgent 将任务委派给对应成员——成员不会自动开始工作。", teamName, len(members)),
	}

	if len(taskIDs) > 0 {
		result["task_ids"] = taskIDs
		result["message"] = fmt.Sprintf("团队 %q 已创建，包含 %d 个成员，并创建了 %d 个任务。下一步：用 SubAgent 将任务委派给对应成员——成员不会自动开始工作。", teamName, len(members), len(taskIDs))
	}

	return result, nil
}

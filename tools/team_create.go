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
		Description: "创建一个代理团队来协作完成复杂任务。团队负责人协调工作并委派给团队成员。",
		Prompt: `创建一个代理团队来协作解决复杂任务。

团队包括：
- 团队负责人（你或指定的代理）负责协调工作
- 团队成员（其他代理）执行任务的特定部分

使用场景：
- 任务对单个代理来说太复杂
- 需要多个专业代理协作
- 你想组织有协调的并行工作流

团队创建是立即的。创建团队后，使用 TaskCreate 创建规划条目，
使用 SubAgent/Agent 工具将工作分派给成员。

必需参数：
- team_name：团队的简短唯一名称（kebab-case，例如 "data-analysis-team"）
- description：团队正在做什么
- leader：团队负责人代理的名称（通常是你自己）
- members：将成为团队成员的代理名称数组

可选参数：
- tasks：为每个成员创建规划条目的任务描述数组

返回：
- team_name、leader、members 列表
- 如果创建了任务则返回 task_ids`,
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
	teamName, _ := params["team_name"].(string)
	if teamName == "" {
		return nil, fmt.Errorf("team_name 不能为空")
	}
	description, _ := params["description"].(string)
	if description == "" {
		return nil, fmt.Errorf("description 不能为空")
	}
	leader, _ := params["leader"].(string)
	if leader == "" {
		return nil, fmt.Errorf("leader 不能为空")
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
		return nil, fmt.Errorf("members 不能为空且必须至少包含一个代理")
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("TeamCreate 需要包含 SessionID 的 ToolContext")
	}

	team := &Team{
		Name:        teamName,
		Description: description,
		Leader:      leader,
		Members:     members,
	}

	if err := CreateTeam(ctx, tc.Session.ID(), team); err != nil {
		return nil, fmt.Errorf("创建团队失败：%w", err)
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
		"message":   fmt.Sprintf("团队 %q 已创建，包含 %d 个成员", teamName, len(members)),
	}

	if len(taskIDs) > 0 {
		result["task_ids"] = taskIDs
		result["message"] = fmt.Sprintf("团队 %q 已创建，包含 %d 个成员，并创建了 %d 个任务", teamName, len(members), len(taskIDs))
	}

	return result, nil
}

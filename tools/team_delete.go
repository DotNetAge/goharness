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
		Description: "删除团队并清理其关联数据。",
		Prompt: `删除团队并清理其数据。

用途：
- 团队完成工作后清理
- 移除不再需要的团队

删除前：
- 所有团队成员应已完成其任务
- 使用 TeamList 验证团队状态`,
		Tags: []string{"team", "delete", "cleanup", "orchestration"},
		Parameters: []Parameter{
			{Name: "team_name", Type: "string", Description: "要删除的团队名称。", Required: true},
		},
	}
}

func (t *TeamDeleteTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawTeamName, _ := GetParam(params, "team_name")
	teamName, _ := rawTeamName.(string)
	if teamName == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("TeamDelete", "team_name"))
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.ID() == "" {
		return nil, fmt.Errorf("%s", GuideMissingContext("TeamDelete", "包含 SessionID 的 ToolContext"))
	}

	team, err := GetTeam(ctx, tc.Session.ID(), teamName)
	if err != nil {
		return nil, err
	}
	if team == nil {
		return nil, fmt.Errorf("%s", GuideNotFound("团队", teamName, "使用 TeamCreate 创建该团队，或用 TeamList 查看现有团队名称后重试"))
	}

	if err := DeleteTeam(ctx, tc.Session.ID(), teamName); err != nil {
		return nil, err
	}

	return map[string]any{
		"success":   true,
		"message":   fmt.Sprintf("团队 %q 已成功删除", teamName),
		"team_name": teamName,
	}, nil
}

package permission

import (
	"fmt"
	"strings"

	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

type SkillBasedChecker struct {
	registry skill.SkillRegistry
}

func NewSkillBasedChecker(registry skill.SkillRegistry) *SkillBasedChecker {
	return &SkillBasedChecker{registry: registry}
}

func (c *SkillBasedChecker) CheckPermissions(ctx *tools.ToolUseContext) PermissionResult {
	if c.registry == nil {
		return PermissionResult{Behavior: PermissionAllow}
	}

	skills := c.registry.ListSkills()
	for _, skill := range skills {
		if skill == nil || skill.AllowedTools == "" {
			continue
		}
		for _, allowed := range strings.Fields(skill.AllowedTools) {
			if strings.EqualFold(allowed, ctx.ToolName) {
				return PermissionResult{
					Behavior: PermissionAllow,
					Message:  fmt.Sprintf("allowed by skill: %s", skill.Name),
				}
			}
		}
	}

	return PermissionResult{Behavior: PermissionAllow}
}

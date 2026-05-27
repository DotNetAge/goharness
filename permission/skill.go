package permission

import (
	"fmt"
	"strings"

	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

// SkillBasedChecker implements permission checking based on skill definitions.
// It checks if a tool is in the AllowedTools list of any registered skill.
// This provides a declarative way to control tool access through skill configuration.
type SkillBasedChecker struct {
	registry skill.SkillRegistry
}

// NewSkillBasedChecker creates a new SkillBasedChecker using the given skill registry.
func NewSkillBasedChecker(registry skill.SkillRegistry) *SkillBasedChecker {
	return &SkillBasedChecker{registry: registry}
}

// CheckPermissions checks if a tool is allowed based on skill definitions.
// Returns PermissionAllow if the tool is found in any skill's AllowedTools list,
// or if no skills are registered (fail-open).
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

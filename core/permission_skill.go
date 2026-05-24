package core

import (
	"fmt"
	"strings"
)

// SkillBasedChecker implements ToolPermissionChecker by checking the current
// SkillRegistry for skills whose AllowedTools includes the requested tool.
//
// When a skill declares allowed-tools: "Read Write Bash", any tool call matching
// those names is pre-approved without asking the user — the skill's declaration
// is treated as an implicit permission grant.
//
// The checker reads the registry on every invocation, so newly registered skills
// take effect immediately ("动态检查"). No caching is performed.
//
// Place this in the PermissionChain before RuleBasedChecker:
//
//	PermissionChain{
//	    SkillBasedChecker(registry),                // [0] pre-approved by skill
//	    RuleBasedChecker(store),                    // [1] explicit rule matching
//	    FallbackPermissionChecker(),                // [2] fallback: ask for sensitive tools
//	}
type SkillBasedChecker struct {
	registry SkillRegistry
}

// NewSkillBasedChecker creates a checker that reads from the given registry.
// Pass nil to create a no-op checker (every check passes through).
func NewSkillBasedChecker(registry SkillRegistry) *SkillBasedChecker {
	return &SkillBasedChecker{registry: registry}
}

// CheckPermissions implements ToolPermissionChecker.
// It returns:
//   - PermissionAllow + msg → the tool is allowed by a skill's AllowedTools
//   - PermissionAllow (empty) → no skill matched, pass through to next checker
func (c *SkillBasedChecker) CheckPermissions(ctx *ToolUseContext) PermissionResult {
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

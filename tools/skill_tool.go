package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goharness/skill"
)

// SkillLookupFunc looks up a skill by name and returns it if found.
// The reactor provides this to avoid circular imports.
type SkillLookupFunc func(name string) (*skill.Skill, error)

// SkillTool lets the LLM load a skill's full instructions on demand.
//
// The tool is called by the LLM when it determines that a listed skill
// (from the SkillsCatalog in System Prompt) is needed for the current task.
// The tool returns the full skill instructions via tool result, which the
// LLM sees in the next round's Observation.
type SkillTool struct {
	lookup SkillLookupFunc
}

// NewSkillTool creates a SkillTool.
// lookup is provided by the reactor.
func NewSkillTool(lookup SkillLookupFunc) *SkillTool {
	return &SkillTool{lookup: lookup}
}

func (t *SkillTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Skill",
		MaxResultSizeChars: 50000,
		Description:        "Load a specialized skill by name. Call this tool when a skill can help with the current task.",
		Prompt: `Load a specialized skill's full instructions by name. Call this when a skill from the capabilities list matches the current task.

The result includes instructions and may include a base directory — use Read to access reference files in that directory.`,
		Tags: []string{"skill", "capability"},
		Parameters: []Parameter{
			{
				Name:        "name",
				Type:        "string",
				Description: "Name of the skill to load (from the available capabilities list).",
				Required:    true,
			},
		},
		IsReadOnly: true,
	}
}

func (t *SkillTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}

	skill, err := t.lookup(name)
	if err != nil {
		return nil, fmt.Errorf("skill %q not found: %w", name, err)
	}

	// Build a comprehensive skill description for the tool result.
	// Resources section (RootDir) comes BEFORE Instructions so it's visible
	// in persisted-result previews (first 2000 bytes of <persisted-output>).
	// result := fmt.Sprintf("=== Skill: %s ===\n\nDescription: %s\n", skill.Name, skill.Description)

	// if skill.RootDir != "" {
	// 	result += fmt.Sprintf("\n=== Resources ===\nBase directory: %s\nUse the Read tool to access files in this directory for detailed reference material, examples, or configuration templates.\n", skill.RootDir)
	// }
	// if skill.AllowedTools != "" {
	// 	result += fmt.Sprintf("\nAllowed tools: %s\n", skill.AllowedTools)
	// }
	// result += fmt.Sprintf("\nInstructions:\n%s", skill.Instructions)

	return map[string]any{
		"skill_name": skill.Name,
		"root_dir":   skill.RootDir,
		"content":    skill.Instructions,
		"loaded":     true,
	}, nil
}

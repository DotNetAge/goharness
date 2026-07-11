package tools

import (
	"context"
	"fmt"
	"strings"

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
		Description:        "按名称加载专业技能。当某个技能有助于完成当前任务时，请调用此工具。",
		Prompt: `按名称加载技能的完整指令。当能力列表中的某个技能与当前任务匹配时，请调用此工具。

返回结果包含指令内容，可能包含基础目录——可使用 Read 工具访问该目录下的参考文件。`,
		Tags: []string{"skill", "capability"},
		Parameters: []Parameter{
			{
				Name:        "name",
				Type:        "string",
				Description: "要加载的技能名称（来自可用能力列表）。",
				Required:    true,
			},
		},
		IsReadOnly: true,
	}
}

func (t *SkillTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("技能名称不能为空")
	}

	skill, err := t.lookup(name)
	if err != nil {
		return nil, fmt.Errorf("技能 %q 未找到：%w", name, err)
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

	result := map[string]any{
		"skill_name": skill.Name,
		"root_dir":   skill.RootDir,
		"content":    skill.Instructions,
		"loaded":     true,
	}

	// Include allowed_tools so ToolActivationHook can auto-activate them.
	if skill.AllowedTools != "" {
		result["allowed_tools"] = strings.Fields(skill.AllowedTools)
	}

	return result, nil
}

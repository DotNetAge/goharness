package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/skill"
)

// SkillLookupFunc looks up a skill by name and returns it if found.
// The reactor provides this to avoid circular imports.
type SkillLookupFunc func(name string) (*skill.Skill, error)

// skillDedupCache 记录已加载的技能名称，防止重复加载浪费 token。
var skillDedupCache sync.Map

// checkSkillLoaded 检查技能是否已加载过。
// 已加载返回 true，未加载则标记后返回 false。
func checkSkillLoaded(name string) bool {
	_, loaded := skillDedupCache.LoadOrStore(name, true)
	return loaded
}

// SkillTool lets the LLM load a skill's full instructions on demand.
//
// The tool is called by the LLM when it determines that a listed skill
// (from the SkillsCatalog in System Prompt) is needed for the current task.
// The tool returns the full skill instructions via tool result, which the
// LLM sees in the next round's Observation.
//
// 改进：增加去重缓存，同一技能多次加载时返回简短提示避免 token 浪费。
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

返回结果包含指令内容，可能包含基础目录——可使用 Read 工具访问该目录下的参考文件。
同一技能只需加载一次，后续重复加载会返回简短提示。`,
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
	rawName, _ := GetParam(params, "name")
	name, ok := rawName.(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("技能名称不能为空")
	}

	// 去重检查：已加载过则返回简短提示
	if checkSkillLoaded(name) {
		return map[string]any{
			"skill_name": name,
			"content":    fmt.Sprintf("技能 %q 已加载。本对话中之前 Skill 工具的结果仍然有效——请引用此前的结果。", name),
			"loaded":     true,
			"_note":      "技能未变化。引用之前的结果。",
		}, nil
	}

	skill, err := t.lookup(name)
	if err != nil {
		return nil, fmt.Errorf("技能 %q 未找到：%w", name, err)
	}

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

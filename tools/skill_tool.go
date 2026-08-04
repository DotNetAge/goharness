package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/skill"
)

// SkillLookupFunc 按名称查找技能，找到则返回。
// reactor 提供此函数以避免循环导入。
type SkillLookupFunc func(name string) (*skill.Skill, error)

// skillDedupCache 记录已加载的技能名称，防止重复加载浪费 token。
var skillDedupCache sync.Map

// checkSkillLoaded 检查技能是否已加载过。
// 已加载返回 true，未加载则标记后返回 false。
func checkSkillLoaded(name string) bool {
	_, loaded := skillDedupCache.LoadOrStore(name, true)
	return loaded
}

// SkillTool 允许 LLM 按需加载技能的完整指令。
//
// 当 LLM 判断当前任务需要某个已列出技能（来自 System Prompt 中的
// SkillsCatalog）时调用此工具。工具通过执行结果返回完整技能指令，
// LLM 在下一轮的 Observation 中可见。
//
// 改进：增加去重缓存，同一技能多次加载时返回简短提示避免 token 浪费。
type SkillTool struct {
	lookup SkillLookupFunc
}

// NewSkillTool 创建一个 SkillTool。
// lookup 由 reactor 提供。
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
	rawName, _ := GetParam(params, "name")
	name, ok := rawName.(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("Skill", "name"))
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
		return nil, fmt.Errorf("%s", GuideNotFound("技能", name, "检查技能名称拼写，从可用能力列表中选取正确的技能名称后重新调用；若该技能确实不存在，应告知用户"))
	}

	result := map[string]any{
		"skill_name": skill.Name,
		"root_dir":   skill.RootDir,
		"content":    skill.Instructions,
		"loaded":     true,
	}

	// 包含 allowed_tools，用于技能加载后的工具激活。
	if skill.AllowedTools != "" {
		result["allowed_tools"] = strings.Fields(skill.AllowedTools)
	}

	return result, nil
}

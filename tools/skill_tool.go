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

// ProjectSkillResolver 按项目目录与名称解析动态技能（军规：工作目录内的
// 技能绝不进入系统提示词，Agent 经 CLI 发现后按名加载）。由应用侧提供实现，
// goharness 不负责 SKILL.md 解析（SPI 收窄：解析职责留在应用侧技能存储）。
// 未命中返回 error。
type ProjectSkillResolver func(projectDir, name string) (*skill.Skill, error)

// skillDedupCache 记录已加载技能的版本指纹（名称 → SkillVersion）。
// 同名且内容未变时返回简短提示避免 token 浪费；内容变化（如进化副本
// 更新）后指纹不再匹配，自动重新完整加载。
var skillDedupCache sync.Map

// SkillVersion 返回技能的版本指纹（根目录+指令内容，内存比对零额外 IO）。
// 用于进化感知判重：同名技能的进化副本更新后指纹变化，Skill 工具据此
// 判定需要重新完整加载而非返回"已加载"提示。
func SkillVersion(sk *skill.Skill) string {
	return sk.RootDir + "\x00" + sk.Instructions
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
	// projectResolver 为可选的动态技能回退解析器（nil 表示不支持动态技能）。
	projectResolver ProjectSkillResolver
}

// NewSkillTool 创建一个 SkillTool。
// lookup 由 reactor 提供；projectResolver 为可选的动态技能回退解析器。
func NewSkillTool(lookup SkillLookupFunc, projectResolver ProjectSkillResolver) *SkillTool {
	return &SkillTool{lookup: lookup, projectResolver: projectResolver}
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

	// 解析顺序：项目级 > Agent 级 > 全局（进化副本同名遮蔽）。
	// 会话绑定项目目录时先按项目级解析（执行期解析不触碰系统提示词，
	// 不影响 KV 缓存前缀）；未命中或未绑定项目时回退基础注册表。
	var sk *skill.Skill
	if t.projectResolver != nil {
		if tc := GetToolContext(ctx); tc.Session != nil {
			if projectDir := tc.Session.ProjectDir(); projectDir != "" {
				// 项目级未命中视为未命中，由下方基础注册表继续解析
				sk, _ = t.projectResolver(projectDir, name)
			}
		}
	}
	if sk == nil {
		var err error
		if sk, err = t.lookup(name); err != nil {
			return nil, fmt.Errorf("%s", GuideNotFound("技能", name, "检查技能名称拼写，从可用能力列表中选取正确的技能名称后重新调用；若该技能确实不存在，应告知用户"))
		}
	}

	// 版本判重：同指纹（同名且内容未变）返回简短提示；
	// 指纹变化（进化副本更新）时重新完整加载。
	if cached, loaded := skillDedupCache.Load(name); loaded && cached == SkillVersion(sk) {
		return map[string]any{
			"skill_name": name,
			"content":    fmt.Sprintf("技能 %q 已加载。本对话中之前 Skill 工具的结果仍然有效——请引用此前的结果。", name),
			"loaded":     true,
			"_note":      "技能未变化。引用之前的结果。",
		}, nil
	}
	skillDedupCache.Store(name, SkillVersion(sk))

	result := map[string]any{
		"skill_name": sk.Name,
		"root_dir":   sk.RootDir,
		"content":    sk.Instructions,
		"loaded":     true,
	}

	// 包含 allowed_tools，用于技能加载后的工具激活。
	if sk.AllowedTools != "" {
		result["allowed_tools"] = strings.Fields(sk.AllowedTools)
	}

	return result, nil
}

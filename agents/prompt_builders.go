package agents

import (
	"fmt"
	"os"
	"strings"

	"github.com/DotNetAge/goharness/skill"
	"github.com/DotNetAge/goharness/tools"
)

// ── Identity ────────────────────────────────────────────────────────────────

func articleFor(word string) string {
	return ""
}

func buildIdentity(name, role, description, introduction string) string {
	if role == "" {
		return fmt.Sprintf("- 名称: %s\n- 职责: %s\n\n%s",
			name, description, introduction)
	}
	return fmt.Sprintf("你是 %s。\n- 名称: %s\n- 职责: %s\n\n%s",
		role, name, description, introduction)
}

// ── Skills Catalog ──────────────────────────────────────────────────────────

func buildSkillsCatalog(skills []*skill.Skill) string {
	if len(skills) == 0 {
		return ""
	}

	header := "## 能力（可用技能）\n" +
		"检查以下专业技能是否可以完成用户的任务要求。如果技能匹配，使用 Skill 工具加载其指令，这将指导你完成特定领域的工作流程并提供额外的工具。\n\n" +
		"### 副作用规则\n" +
		"- Skill 工具的返回值代表该技能的完整知识。对于任何给定的技能名称，每个会话中最多只能调用 Skill 一次。之后，对该技能内容的所有引用必须依赖内存中已有的内容 — 不要使用任何工具（Bash、Read、Grep、Glob、WebFetch 等）重新读取其文件。\n\n" +
		"### 执行前自检\n" +
		"在调用 Bash、Read 或 Grep 访问文件或目录内容之前，必须先执行此检查：\n" +
		"1. 角色门控 (P0)：此任务是否在我的职责范围内？如果否 → 按行为准则委托，不要继续。\n" +
		"2. 如果在职责范围内：上述能力列表是否包含覆盖此任务的技能？\n" +
		"3. 如果是，我是否已通过 Skill 加载？\n" +
		"4. 输出你的推理和决策：\n" +
		"   - 推理：[职责检查结果 + 考虑了哪个技能]\n" +
		"   - 决策：委托（如果超出职责）| Skill（如果尚未加载）| 使用工具继续（如果已加载或无匹配技能）\n"

	footer := "\n### 加载策略\n" +
		"- 延迟加载技能：仅在即将执行需要它的任务时加载\n"
	const SKILL_CATALOG_BUDGET = 3000
	budgetRemaining := SKILL_CATALOG_BUDGET - len(header) - len(footer)
	if budgetRemaining <= 0 {
		return header + footer
	}

	var bundled, userSkills []*skill.Skill
	for _, s := range skills {
		if s.Source == "bundled" {
			bundled = append(bundled, s)
		} else {
			userSkills = append(userSkills, s)
		}
	}

	var entryBuilder strings.Builder
	buildEntry := func(s *skill.Skill, fullDesc bool) string {
		entry := "- " + s.Name
		if fullDesc && s.Description != "" {
			entry += ": " + s.Description
		}
		entry += "\n"
		return entry
	}

	for _, s := range bundled {
		entry := buildEntry(s, true)
		if len(entry) <= budgetRemaining {
			entryBuilder.WriteString(entry)
			budgetRemaining -= len(entry)
		}
	}

	for _, s := range userSkills {
		fullEntry := buildEntry(s, true)
		if len(fullEntry) <= budgetRemaining {
			entryBuilder.WriteString(fullEntry)
			budgetRemaining -= len(fullEntry)
			continue
		}
		nameEntry := buildEntry(s, false)
		if len(nameEntry) <= budgetRemaining {
			entryBuilder.WriteString(nameEntry)
			budgetRemaining -= len(nameEntry)
		}
	}

	return header + entryBuilder.String() + footer
}

// ── Environment Info ────────────────────────────────────────────────────────

const directorySemanticsPrompt = "## 文件操作指南\n\n" +
	"### 项目目录 (%s)\n" +
	"**默认工作区 — 文件永久保留。**\n" +
	"用于：源代码、配置、文档以及用户可能希望保留或稍后查看的所有输出。\n" +
	"除非有充分理由，否则大部分工作（读取、写入、创建文件）都应在此进行。\n\n" +
	"### 会话目录 (%s)\n" +
	"**临时空间 — 对话结束时删除。**\n" +
	"仅用于：一次性丢弃的输出、中间草稿文件、在此对话之外没有价值的快速实验。\n\n" +
	"### 快速规则\n" +
	"- 修改用户现有文件？→ 项目目录 | 生成有用内容？→ 项目目录\n" +
	"- 真正的临时草稿（草稿、实验）？→ 会话目录\n" +
	"- 不确定？→ 默认使用**项目目录** — 保留比丢失工作更安全\n" +
	"- 路径语法：相对路径 → 项目目录 | `session:<path>` → 会话目录\n" +
	"- 未经读取切勿覆盖项目文件\n"

func buildDirectoryUsageGuidance(projectDir, sessionDir string) string {
	return fmt.Sprintf(directorySemanticsPrompt, projectDir, sessionDir)
}

type EnvsParams struct {
	ProjectDir string
	SessionDir string
	SessionID  string
}

func buildEnvironmentInfo(params EnvsParams) string {
	projectDir := params.ProjectDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	var directoryGuidance string
	if params.ProjectDir != "" && params.SessionDir != "" {
		directoryGuidance = buildDirectoryUsageGuidance(params.ProjectDir, params.SessionDir)
	}

	return fmt.Sprintf("## 环境\n"+
		"- **项目目录**: %s（持久化工作区）\n"+
		"- **会话目录**: %s（临时工作区，对话结束后清除）\n"+
		"- **快速规则**: 修改用户文件 → 项目目录 | 我的输出 → 会话目录 | 不确定时 → 默认使用会话目录\n"+
		"%s",
		projectDir,
		params.SessionDir,
		directoryGuidance)
}

// ── Tool Catalog ────────────────────────────────────────────────────────────

// buildToolCatalog generates the Tool Catalog section for System Prompt.
// Lists every registered tool as "name - description" so the LLM knows what
// tools are available and can request them via ToolSelector.
//
// This section is informational only — tool schemas are NOT loaded at this point.
// The LLM must call ToolSelector to activate specific tools.
func buildToolCatalog(registry tools.ToolRegistry) string {
	allTools := registry.All()
	if len(allTools) == 0 {
		return ""
	}

	// Exclude core meta-tools that are always loaded and should not appear
	// in the catalog (ToolSelector, Skill, AskUser).
	exclude := map[string]bool{
		"ToolSelector":   true,
		"Skill":          true,
		"AskUser":        true,
		"SubAgent":       true,
		"CollectResults": true,
	}

	var buf strings.Builder
	buf.WriteString("## 可用工具目录\n")
	buf.WriteString("以下工具可用但尚未加载。")
	buf.WriteString("要使用其中任何工具，请使用其确切名称调用 ToolSelector。")
	buf.WriteString("你可以一次选择多个工具以减少往返次数。\n\n")

	listed := 0
	for _, t := range allTools {
		info := t.Info()
		if exclude[info.Name] {
			continue
		}
		buf.WriteString(fmt.Sprintf("- %s: %s\n", info.Name, info.Description))
		listed++
	}

	if listed == 0 {
		return ""
	}
	return buf.String()
}

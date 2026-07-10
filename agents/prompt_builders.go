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
	if len(word) == 0 {
		return "a"
	}
	r := []rune(word)[0]
	switch r {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an"
	default:
		return "a"
	}
}

func buildIdentity(name, role, description, introduction string) string {
	if role == "" {
		return fmt.Sprintf("- Name: %s\n- Responsibility: %s\n\n%s",
			name, description, introduction)
	}
	return fmt.Sprintf("You are %s %s.\n- Name: %s\n- Responsibility: %s\n\n%s",
		articleFor(role), role, name, description, introduction)
}

// ── Skills Catalog ──────────────────────────────────────────────────────────

func buildSkillsCatalog(skills []*skill.Skill) string {
	if len(skills) == 0 {
		return ""
	}

	header := "## Capacities (Available Skills)\n" +
		"When your existing tools cannot fully address the user's request, check whether one of the following specialized skills covers the domain. If a skill matches, use the Skill tool to load its instructions, which will guide you through domain-specific workflows and expose additional tools.\n\n" +
		"### Side-Effect Rules\n" +
		"- The Skill() tool's return value represents the complete knowledge of that skill. For any given skill name, you may call Skill() at most ONCE per session. After that, all references to that skill's content MUST rely on what is already in memory — do NOT use any tool (Bash, Read, Grep, Glob, WebFetch, etc.) to re-read its files.\n\n" +
		"### Pre-Execution Self-Check\n" +
		"Before calling Bash, Read, or Grep to access file or directory content, you MUST run this check first:\n" +
		"1. Role Gate (P0): is this task within my remit? If NO → delegate per Behavioral Rules, do NOT proceed.\n" +
		"2. If within remit: does the Capacities list above contain a Skill that covers this task?\n" +
		"3. If yes, have I already loaded it via Skill()?\n" +
		"4. Output your reasoning and decision:\n" +
		"   - Reasoning: [remit check result + which Skill was considered]\n" +
		"   - Decision: delegate (if outside remit) | Skill() (if not yet loaded) | proceed with tools (if loaded or no matching Skill)\n"

	footer := "\n### Loading Strategy\n" +
		"- Load skills LAZILY: only when you're about to perform a task that requires it\n"
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

const directorySemanticsPrompt = "## File Operation Guidelines\n\n" +
	"### Project Directory (%s)\n" +
	"**Default workspace — files persist permanently.**\n" +
	"Use for: source code, configs, docs, and all outputs the user may want to keep or review later.\n" +
	"Most of your work (reading, writing, creating files) should happen here unless there's a\n" +
	"strong reason not to.\n\n" +
	"### Session Directory (%s)\n" +
	"**Ephemeral temp space — deleted when the conversation ends.**\n" +
	"Use ONLY for: one-off throwaway outputs, intermediate scratch files, quick experiments\n" +
	"that have no value beyond this chat.\n\n" +
	"### Quick Rules\n" +
	"- Modifying user's existing files? \u2192 Project Dir | Producing something useful? \u2192 Project Dir\n" +
	"- Truly temporary scratch (drafts, experiments)? \u2192 Session Dir\n" +
	"- Unsure? \u2192 default to **Project Dir** — it is safer to persist than to lose work\n" +
	"- Path syntax: relative paths \u2192 Project Dir | `session:<path>` \u2192 Session Dir\n" +
	"- Never overwrite Project files without reading them first\n"

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

	return fmt.Sprintf("## Environment\n"+
		"- **Project Dir**: %s (persistent workspace)\n"+
		"- **Session Dir**: %s (ephemeral temp workspace, cleared after conversation)\n"+
		"- **Quick Rule**: Modifying user's files \u2192 Project | My outputs \u2192 Session | When unsure \u2192 default to Session\n"+
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
	buf.WriteString("## Available Tool Catalog\n")
	buf.WriteString("The tools below are available but NOT yet loaded. ")
	buf.WriteString("To use any of them, call ToolSelector with their exact names. ")
	buf.WriteString("You can select multiple tools at once to minimize round trips.\n\n")

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

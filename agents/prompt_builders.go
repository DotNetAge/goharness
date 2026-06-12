package agents

import (
	"fmt"
	"os"
	"strings"

	"github.com/DotNetAge/goharness/skill"
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
		"When your existing tools cannot fully address the user's request, check whether one of the following specialized skills covers the domain. If a skill matches, use the Skill tool to load its instructions, which will guide you through domain-specific workflows and expose additional tools.\n"

	footer := "\n### Loading Strategy\n" +
		"- Load skills LAZILY: only when you're about to perform a task that requires it\n" +
		"- Each skill persists once loaded into conversation context \u2014 do NOT reload already-loaded skills\n"

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
	"You have two workspaces:\n\n" +
	"### Project Directory (%s)\n" +
	"**Persistent \u2014 files survive after session ends.**\n" +
	"Use for: source code, configs, docs, anything user expects to keep long-term.\n\n" +
	"### Session Directory (%s)\n" +
	"**Ephemeral sandbox \u2014 temporary workspace for this conversation only.**\n" +
	"Use for: drafts, reports, analysis, temp files, artifacts generated during this chat.\n\n" +
	"### Quick Rules\n" +
	"- Modifying user's existing files? \u2192 Project Dir | Your outputs? \u2192 Session Dir | Unsure? \u2192 default to Session\n" +
	"- Path syntax: relative paths \u2192 Project Dir | `session:<path>` \u2192 Session Dir\n" +
	"- Never overwrite Project files without reading them first\n"

func buildDirectoryUsageGuidance(projectDir, sessionDir string) string {
	return fmt.Sprintf(directorySemanticsPrompt, projectDir, sessionDir)
}

type environmentInfoParams struct {
	ProjectDir string
	SessionDir string
	SessionID  string
}

func buildEnvironmentInfo(params environmentInfoParams) string {
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

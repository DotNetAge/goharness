package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const frontmatterDelimiter = "---"

func ValidateSkillName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("skill name must be 1-64 characters, got %d", len(name))
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("skill name must not start or end with a hyphen: %q", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("skill name must not contain consecutive hyphens: %q", name)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return fmt.Errorf("skill name must only contain lowercase letters, numbers, and hyphens: %q", name)
		}
	}
	return nil
}

func ValidateSkillDescription(desc string) error {
	if len(desc) < 1 || len(desc) > 1024 {
		return fmt.Errorf("skill description must be 1-1024 characters, got %d", len(desc))
	}
	return nil
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

func parseYamlFrontmatter(content string) (skillFrontmatter, string, error) {
	var fm skillFrontmatter

	content = strings.TrimLeft(content, "\n\r")
	if !strings.HasPrefix(content, frontmatterDelimiter) {
		return fm, content, fmt.Errorf("SKILL.md must start with YAML frontmatter (---)")
	}

	rest := content[len(frontmatterDelimiter):]
	closeIdx := strings.Index(rest, "\n"+frontmatterDelimiter)
	if closeIdx < 0 {
		return fm, content, fmt.Errorf("SKILL.md has unclosed YAML frontmatter (missing closing ---)")
	}

	yamlBlock := rest[:closeIdx]
	body := rest[closeIdx+len(frontmatterDelimiter)+1:]

	if err := parseSimpleYaml(yamlBlock, &fm); err != nil {
		return fm, body, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	return fm, body, nil
}

func parseSimpleYaml(yaml string, fm *skillFrontmatter) error {
	lines := strings.Split(yaml, "\n")
	var currentMapKey string
	var multilineKey string
	var multilineValue strings.Builder

	for _, line := range lines {
		rawLine := line
		line = strings.TrimSpace(line)

		if multilineKey != "" {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if isNewTopLevelKey(line) {
				flushMultiline(multilineKey, strings.TrimSpace(multilineValue.String()), fm)
				multilineKey = ""
				multilineValue.Reset()
			} else {
				if multilineValue.Len() > 0 {
					multilineValue.WriteByte(' ')
				}
				multilineValue.WriteString(line)
				continue
			}
		}

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			value = strings.Trim(value, `"'`)

			if value == "|" || value == ">" {
				multilineKey = key
				multilineValue.Reset()
				continue
			}

			switch key {
			case "name":
				fm.Name = value
			case "description":
				if value != "" {
					fm.Description = value
				}
			case "license":
				fm.License = value
			case "compatibility":
				fm.Compatibility = value
			case "allowed-tools":
				fm.AllowedTools = value
			case "metadata":
				if fm.Metadata == nil {
					fm.Metadata = make(map[string]string)
				}
				currentMapKey = "metadata"
				if value != "" && value != "{" {
					parseInlineMap(value, fm.Metadata)
				}
			default:
			}
		} else if currentMapKey != "" && strings.HasPrefix(rawLine, "  ") || strings.Contains(line, ":") {
			if subIdx := strings.Index(line, ":"); subIdx > 0 {
				subKey := strings.TrimSpace(line[:subIdx])
				subVal := strings.TrimSpace(line[subIdx+1:])
				subVal = strings.Trim(subVal, `"'`)
				if currentMapKey == "metadata" && fm.Metadata != nil {
					fm.Metadata[subKey] = subVal
				}
			}
		}
	}

	if multilineKey != "" {
		flushMultiline(multilineKey, strings.TrimSpace(multilineValue.String()), fm)
	}

	return nil
}

func isNewTopLevelKey(line string) bool {
	if idx := strings.Index(line, ":"); idx > 1 {
		key := line[:idx]
		for _, r := range key {
			if r != ' ' && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
				return false
			}
		}
		return true
	}
	return false
}

func flushMultiline(key, value string, fm *skillFrontmatter) {
	switch key {
	case "description":
		if value != "" {
			fm.Description = value
		}
	case "allowed-tools":
		fm.AllowedTools = value
	default:
	}
}

func parseInlineMap(s string, m map[string]string) {
	for pair := range strings.SplitSeq(s, ",") {
		pair = strings.TrimSpace(pair)
		if idx := strings.Index(pair, ":"); idx > 0 {
			k := strings.TrimSpace(pair[:idx])
			v := strings.TrimSpace(pair[idx+1:])
			v = strings.Trim(v, `"'`)
			m[k] = v
		}
	}
}

type FileSystemSkillLoader struct {
	RootDir string
}

func NewFileSystemSkillLoader(rootDir string) *FileSystemSkillLoader {
	return &FileSystemSkillLoader{RootDir: rootDir}
}

func (l *FileSystemSkillLoader) Load() ([]*Skill, error) {
	entries, err := os.ReadDir(l.RootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read skill directory %q: %w", l.RootDir, err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(l.RootDir, entry.Name())
		skill, err := loadSkillFromDir(skillDir, "filesystem")
		if err != nil {
			return nil, fmt.Errorf("failed to load skill from %q: %w", skillDir, err)
		}
		if skill != nil {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

func loadSkillFromDir(dir string, source string) (*Skill, error) {
	skillMdPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read SKILL.md: %w", err)
	}
	return parseSkillMd(data, dir, source)
}

func parseSkillMd(data []byte, rootDir string, source string) (*Skill, error) {
	content := string(data)
	fm, body, err := parseYamlFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SKILL.md frontmatter: %w", err)
	}

	if fm.Name == "" {
		return nil, fmt.Errorf("SKILL.md is missing required 'name' field in frontmatter")
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("SKILL.md is missing required 'description' field in frontmatter")
	}

	if err := ValidateSkillName(fm.Name); err != nil {
		sanitized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fm.Name), " ", "-"))
		if err := ValidateSkillName(sanitized); err != nil {
			return nil, err
		}
		fm.Name = sanitized
	}
	if err := ValidateSkillDescription(fm.Description); err != nil {
		return nil, err
	}

	instructions := strings.TrimSpace(body)

	resolved := instructions
	if strings.Contains(resolved, "{base_dir}") {
		resolved = strings.ReplaceAll(resolved, "{base_dir}", rootDir)
	}
	if strings.Contains(resolved, "{skill_name}") {
		resolved = strings.ReplaceAll(resolved, "{skill_name}", fm.Name)
	}

	return &Skill{
		Name:          fm.Name,
		Description:   fm.Description,
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		AllowedTools:  fm.AllowedTools,
		Instructions:  resolved,
		RootDir:       rootDir,
		Source:        source,
	}, nil
}

package skill

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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

// AllowedToolsList can be either a string or a list of strings in YAML.
// When unmarshaled from a list, items are joined with spaces.
type AllowedToolsList string

func (a *AllowedToolsList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try as string first
	var s string
	if err := unmarshal(&s); err == nil {
		*a = AllowedToolsList(s)
		return nil
	}
	// Try as list
	var list []string
	if err := unmarshal(&list); err == nil {
		*a = AllowedToolsList(strings.Join(list, " "))
		return nil
	}
	return fmt.Errorf("allowed-tools must be a string or list of strings")
}

type skillFrontmatter struct {
	Name          string           `yaml:"name"`
	Description   string           `yaml:"description"`
	License       string           `yaml:"license,omitempty"`
	Compatibility string           `yaml:"compatibility,omitempty"`
	Metadata      map[string]any   `yaml:"metadata,omitempty"`
	AllowedTools  AllowedToolsList `yaml:"allowed-tools,omitempty"`
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

	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return fm, body, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	return fm, body, nil
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
		skill, err := LoadSkillFromDir(skillDir, "filesystem")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[skill loader] warning: skipping %q: %v\n", skillDir, err)
			continue
		}
		if skill != nil {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

// LoadSkillFromDir loads a single skill from the given directory.
// It reads SKILL.md, parses the YAML frontmatter, verifies dependencies,
// and returns the Skill. If the directory does not contain a SKILL.md,
// it returns nil, nil. If dependencies are not met, it returns an error
// so the caller can skip the skill.
func LoadSkillFromDir(dir string, source string) (*Skill, error) {
	skillMdPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read SKILL.md: %w", err)
	}
	skill, err := parseSkillMd(data, dir, source)
	if err != nil {
		return nil, err
	}

	if err := verifyDependencies(skill); err != nil {
		return nil, fmt.Errorf("dependency check failed for skill %q: %w", skill.Name, err)
	}

	// If a LICENSE.txt file exists in the skill directory, read its full content
	// into the License field (overrides any license: value from YAML frontmatter)
	licensePath := filepath.Join(dir, "LICENSE.txt")
	if _, err := os.Stat(licensePath); err == nil {
		licenseData, readErr := os.ReadFile(licensePath)
		if readErr == nil {
			skill.License = string(licenseData)
		}
	}

	return skill, nil
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

	// Extract requires from metadata (as per spec, all extensions go in metadata)
	var requires *Requires
	if reqVal, ok := fm.Metadata["requires"]; ok {
		if reqMap, ok := reqVal.(map[string]any); ok {
			r := &Requires{}
			if binsVal, ok := reqMap["bins"]; ok {
				if binsList, ok := binsVal.([]any); ok {
					for _, b := range binsList {
						if binStr, ok := b.(string); ok {
							r.Bins = append(r.Bins, binStr)
						}
					}
				}
			}
			if envVal, ok := reqMap["env"]; ok {
				if envList, ok := envVal.([]any); ok {
					for _, e := range envList {
						if envStr, ok := e.(string); ok {
							r.Env = append(r.Env, envStr)
						}
					}
				}
			}
			if len(r.Bins) > 0 || len(r.Env) > 0 {
				requires = r
			}
		}
	}

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
		AllowedTools:  string(fm.AllowedTools),
		Requires:      requires,
		Instructions:  resolved,
		RootDir:       rootDir,
		Source:        source,
	}, nil
}

// verifyDependencies checks declared runtime dependencies.
// Returns nil if all dependencies are met or no dependencies are declared.
// Returns an error if any required binary is missing or required env var is empty.
func verifyDependencies(s *Skill) error {
	if s.Requires == nil {
		return nil
	}

	for _, bin := range s.Requires.Bins {
		bin = strings.TrimSpace(bin)
		if bin == "" {
			continue
		}
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary %q not found on PATH", bin)
		}
	}

	for _, key := range s.Requires.Env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if val := os.Getenv(key); val == "" {
			return fmt.Errorf("required environment variable %q is not set or empty", key)
		}
	}

	return nil
}

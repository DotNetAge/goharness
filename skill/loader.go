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
		return fmt.Errorf("技能名称长度必须为 1-64 个字符，实际为 %d", len(name))
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("技能名称不能以连字符开头或结尾：%q", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("技能名称不能包含连续的连字符：%q", name)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return fmt.Errorf("技能名称只能包含小写字母、数字和连字符：%q", name)
		}
	}
	return nil
}

func ValidateSkillDescription(desc string) error {
	if len(desc) < 1 || len(desc) > 1024 {
		return fmt.Errorf("技能描述长度必须为 1-1024 个字符，实际为 %d", len(desc))
	}
	return nil
}

// AllowedToolsList 在 YAML 中可以是字符串或字符串列表。
// 当从列表反序列化时，元素以空格连接。
type AllowedToolsList string

func (a *AllowedToolsList) UnmarshalYAML(unmarshal func(any) error) error {
	// 先尝试作为字符串解析
	var s string
	if err := unmarshal(&s); err == nil {
		*a = AllowedToolsList(s)
		return nil
	}
	// 再尝试作为列表解析
	var list []string
	if err := unmarshal(&list); err == nil {
		*a = AllowedToolsList(strings.Join(list, " "))
		return nil
	}
	return fmt.Errorf("allowed-tools 必须是字符串或字符串列表")
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
		return fm, content, fmt.Errorf("SKILL.md 必须以 YAML 前置元数据（---）开头")
	}

	rest := content[len(frontmatterDelimiter):]
	closeIdx := strings.Index(rest, "\n"+frontmatterDelimiter)
	if closeIdx < 0 {
		return fm, content, fmt.Errorf("SKILL.md 的 YAML 前置元数据未闭合（缺少结尾的 ---）")
	}

	yamlBlock := rest[:closeIdx]
	body := rest[closeIdx+len(frontmatterDelimiter)+1:]

	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return fm, body, fmt.Errorf("解析 YAML 前置元数据失败：%w", err)
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
		return nil, fmt.Errorf("读取技能目录 %q 失败：%w", l.RootDir, err)
	}

	var skills []*Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(l.RootDir, entry.Name())
		skill, err := LoadSkillFromDir(skillDir, "filesystem")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[技能加载器] 警告：跳过 %s: %v\n", skillDir, err)
			continue
		}
		if skill != nil {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

// LoadSkillFromDir 从给定目录加载单个技能。
// 它会读取 SKILL.md、解析 YAML 前置元数据、校验依赖，并返回 Skill。
// 如果目录不包含 SKILL.md，返回 nil, nil。如果依赖未满足，返回错误，
// 以便调用方跳过该技能。
func LoadSkillFromDir(dir string, source string) (*Skill, error) {
	skillMdPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 SKILL.md 失败：%w", err)
	}
	skill, err := parseSkillMd(data, dir, source)
	if err != nil {
		return nil, err
	}

	if err := verifyDependencies(skill); err != nil {
		return nil, fmt.Errorf("技能 %q 的依赖检查失败：%w", skill.Name, err)
	}

	// 如果技能目录中存在 LICENSE.txt 文件，将其完整内容读入 License 字段
	// （覆盖 YAML 前置元数据中的 license: 值）
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
		return nil, fmt.Errorf("解析 SKILL.md 前置元数据失败：%w", err)
	}

	if fm.Name == "" {
		return nil, fmt.Errorf("SKILL.md 缺少前置元数据中必需的 'name' 字段")
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("SKILL.md 缺少前置元数据中必需的 'description' 字段")
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

	// 从 metadata 中提取 requires（根据规范，所有扩展都放在 metadata 中）
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

// verifyDependencies 检查声明的运行时依赖。
// 所有依赖均满足或未声明依赖时返回 nil。
// 任何必需的二进制文件缺失或必需的环境变量为空时返回错误。
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
			return fmt.Errorf("在 PATH 中未找到必需的二进制文件 %q", bin)
		}
	}

	for _, key := range s.Requires.Env {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if val := os.Getenv(key); val == "" {
			return fmt.Errorf("必需的环境变量 %q 未设置或为空", key)
		}
	}

	return nil
}

package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// ruleYAML 是 YAML 规则文件解析的内部结构。
type ruleYAML struct {
	Rules []Rule `yaml:"rules"`
}

// YAMLRuleRegistry 通过从 YAML 文件加载规则来实现 RuleRegistry。
// 通过读写锁保证并发访问的线程安全。
type YAMLRuleRegistry struct {
	mu    sync.RWMutex
	rules []Rule
}

// NewYAMLRuleRegistry 通过从给定 YAML 文件路径加载规则来创建新的 YAMLRuleRegistry。
func NewYAMLRuleRegistry(yamlPath string) (*YAMLRuleRegistry, error) {
	absPath, err := filepath.Abs(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("解析路径: %w", err)
	}
	reg := &YAMLRuleRegistry{}
	if err := reg.load(absPath); err != nil {
		return nil, err
	}
	return reg, nil
}

// MustYAMLRuleRegistry 创建新的 YAMLRuleRegistry，出错时 panic。
// 适用于包级变量的初始化场景。
func MustYAMLRuleRegistry(yamlPath string) *YAMLRuleRegistry {
	reg, err := NewYAMLRuleRegistry(yamlPath)
	if err != nil {
		panic(fmt.Sprintf("从 %s: %v 加载规则失败", yamlPath, err))
	}
	return reg
}

// load 从 YAML 文件读取并解析规则。
// 校验所有规则的 ID 和 Intro 字段均非空。
func (r *YAMLRuleRegistry) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取文件: %w", err)
	}

	var ry ruleYAML
	if err := yaml.Unmarshal(data, &ry); err != nil {
		return fmt.Errorf("解析规则配置文件: %w", err)
	}

	for i := range ry.Rules {
		if ry.Rules[i].ID == "" {
			return fmt.Errorf("规则 %d 为空ID", i)
		}
		if ry.Rules[i].Intro == "" {
			return fmt.Errorf("规则 %s 的简介为空", ry.Rules[i].ID)
		}
	}

	r.mu.Lock()
	r.rules = ry.Rules
	r.mu.Unlock()
	return nil
}

// Register 在注册表中添加或更新一条规则。
// 若存在相同 ID 的规则，则更新该规则。
func (r *YAMLRuleRegistry) Register(rule Rule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rules {
		if r.rules[i].ID == rule.ID {
			r.rules[i] = rule
			return nil
		}
	}
	r.rules = append(r.rules, rule)
	return nil
}

// Unregister 按 ID 从注册表中移除一条规则。
// 若规则不存在则为空操作。
func (r *YAMLRuleRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rules {
		if r.rules[i].ID == id {
			r.rules = append(r.rules[:i], r.rules[i+1:]...)
			return
		}
	}
}

// Get 按 ID 检索一条规则。找到时返回该规则和 true，否则返回 nil 和 false。
func (r *YAMLRuleRegistry) Get(id string) (*Rule, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.rules {
		if r.rules[i].ID == id {
			return &r.rules[i], true
		}
	}
	return nil, false
}

// All 返回注册表中所有规则的副本。
func (r *YAMLRuleRegistry) All() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Rule, len(r.rules))
	copy(out, r.rules)
	return out
}

// GetByScope 返回匹配给定范围的所有规则。
func (r *YAMLRuleRegistry) GetByScope(scope RuleScope) []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var filtered []Rule
	for _, rule := range r.rules {
		if rule.Scope == scope {
			filtered = append(filtered, rule)
		}
	}
	return filtered
}

// FormatPromptSection 将已启用的规则格式化为 Markdown 列表，以便嵌入到 system prompts 中。
// 若未定义任何规则或所有规则都被禁用，则返回空字符串。
func (r *YAMLRuleRegistry) FormatPromptSection() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.rules) == 0 {
		return ""
	}
	var result string
	for _, rule := range r.rules {
		if rule.Enabled {
			result += "- " + rule.Intro + "\n"
		}
	}
	return result
}

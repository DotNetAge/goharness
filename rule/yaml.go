package rule

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// ruleYAML is the internal structure for YAML rule file parsing.
type ruleYAML struct {
	Rules []Rule `yaml:"rules"`
}

// YAMLRuleRegistry implements RuleRegistry by loading rules from a YAML file.
// Thread-safe with read-write locking for concurrent access.
type YAMLRuleRegistry struct {
	mu    sync.RWMutex
	rules []Rule
}

// NewYAMLRuleRegistry creates a new YAMLRuleRegistry by loading rules from the given YAML file path.
func NewYAMLRuleRegistry(yamlPath string) (*YAMLRuleRegistry, error) {
	absPath, err := filepath.Abs(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	reg := &YAMLRuleRegistry{}
	if err := reg.load(absPath); err != nil {
		return nil, err
	}
	return reg, nil
}

// MustYAMLRuleRegistry creates a new YAMLRuleRegistry or panics on error.
// Useful for initialization in package-level variables.
func MustYAMLRuleRegistry(yamlPath string) *YAMLRuleRegistry {
	reg, err := NewYAMLRuleRegistry(yamlPath)
	if err != nil {
		panic(fmt.Sprintf("failed to load rules from %s: %v", yamlPath, err))
	}
	return reg
}

// load reads and parses rules from a YAML file.
// Validates that all rules have non-empty ID and Intro fields.
func (r *YAMLRuleRegistry) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var ry ruleYAML
	if err := yaml.Unmarshal(data, &ry); err != nil {
		return fmt.Errorf("unmarshal yaml: %w", err)
	}

	for i := range ry.Rules {
		if ry.Rules[i].ID == "" {
			return fmt.Errorf("rule at index %d has empty ID", i)
		}
		if ry.Rules[i].Intro == "" {
			return fmt.Errorf("rule %q has empty intro", ry.Rules[i].ID)
		}
	}

	r.mu.Lock()
	r.rules = ry.Rules
	r.mu.Unlock()
	return nil
}

// Register adds or updates a rule in the registry.
// If a rule with the same ID exists, it is updated.
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

// Unregister removes a rule from the registry by ID.
// No-op if the rule doesn't exist.
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

// Get retrieves a rule by ID. Returns the rule and true if found, nil and false otherwise.
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

// All returns a copy of all rules in the registry.
func (r *YAMLRuleRegistry) All() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Rule, len(r.rules))
	copy(out, r.rules)
	return out
}

// GetByScope returns all rules matching the given scope.
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

// FormatPromptSection formats enabled rules as a Markdown list for inclusion in system prompts.
// Returns empty string if no rules are defined or all rules are disabled.
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

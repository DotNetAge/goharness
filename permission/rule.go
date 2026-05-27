package permission

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/tools"
)

type MatchResult struct {
	Matched  bool
	Behavior rule.RuleBehavior
	Rule     *rule.PermissionRule
	Message  string
}

type RuleMatcher struct {
	rules *rule.PermissionRules
}

func NewRuleMatcher(rules *rule.PermissionRules) *RuleMatcher {
	return &RuleMatcher{rules: rules}
}

func (m *RuleMatcher) Match(toolName string, params map[string]any) MatchResult {
	rules := m.rules
	if rules == nil {
		return MatchResult{}
	}

	for _, r := range rules.AlwaysDeny {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: rule.RuleDeny,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "denied"),
			}
		}
	}

	for _, r := range rules.AlwaysAllow {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: rule.RuleAllow,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "allowed"),
			}
		}
	}

	for _, r := range rules.AlwaysAsk {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: rule.RuleAsk,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "requires approval"),
			}
		}
	}

	return MatchResult{}
}

func ruleMatches(r *rule.PermissionRule, toolName string, params map[string]any) bool {
	if r.ToolName != "" && r.ToolName != "*" && r.ToolName != toolName {
		return false
	}
	if r.ContentPattern == "" {
		return true
	}
	content := extractContentParam(toolName, params)
	if content == "" {
		return false
	}
	return matchContent(r.ContentPattern, content)
}

func extractContentParam(toolName string, params map[string]any) string {
	if cmd, ok := params["command"]; ok {
		if s, ok := cmd.(string); ok {
			return s
		}
	}
	for _, key := range []string{"file_path", "path", "filePath"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	if url, ok := params["url"]; ok {
		if s, ok := url.(string); ok {
			return s
		}
	}
	return ""
}

func matchContent(pattern, content string) bool {
	if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
		if matched, err := filepath.Match(pattern, content); err == nil && matched {
			return true
		}
		prefix := strings.SplitN(pattern, "*", 2)[0]
		prefix = strings.SplitN(prefix, "?", 2)[0]
		if prefix != "" && strings.HasPrefix(content, prefix) {
			return true
		}
		return false
	}
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(content, pattern) {
		return true
	}
	if strings.Contains(content, pattern) {
		return true
	}
	return false
}

func buildRuleMessage(r *rule.PermissionRule, action string) string {
	if r.Description != "" {
		return fmt.Sprintf("rule %s: %s", action, r.Description)
	}
	if r.ContentPattern != "" {
		return fmt.Sprintf("rule %s: %s(%s)", action, r.ToolName, r.ContentPattern)
	}
	return fmt.Sprintf("rule %s: %s", action, r.ToolName)
}

type RuleBasedChecker struct {
	store rule.PermissionRuleStore
	mu    sync.RWMutex
	rules *rule.PermissionRules
}

func NewRuleBasedChecker(store rule.PermissionRuleStore) *RuleBasedChecker {
	return &RuleBasedChecker{store: store}
}

func (c *RuleBasedChecker) CheckPermissions(ctx *tools.ToolUseContext) PermissionResult {
	rules, err := c.getRules()
	if err != nil || rules == nil {
		return PermissionResult{Behavior: PermissionAllow}
	}
	matcher := NewRuleMatcher(rules)
	match := matcher.Match(ctx.ToolName, ctx.Params)
	if !match.Matched {
		return PermissionResult{Behavior: PermissionAllow}
	}
	switch match.Behavior {
	case rule.RuleDeny:
		return PermissionResult{Behavior: PermissionDeny, Message: match.Message}
	case rule.RuleAllow:
		return PermissionResult{Behavior: PermissionAllow, Message: match.Message}
	case rule.RuleAsk:
		return PermissionResult{Behavior: PermissionAsk, Message: match.Message}
	default:
		return PermissionResult{Behavior: PermissionAllow}
	}
}

func (c *RuleBasedChecker) Refresh() {
	c.mu.Lock()
	c.rules = nil
	c.mu.Unlock()
}

func (c *RuleBasedChecker) getRules() (*rule.PermissionRules, error) {
	c.mu.RLock()
	if c.rules != nil {
		c.mu.RUnlock()
		return c.rules, nil
	}
	c.mu.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rules != nil {
		return c.rules, nil
	}
	rules, err := c.store.Load()
	if err != nil {
		return nil, fmt.Errorf("load permission rules: %w", err)
	}
	c.rules = rules
	return c.rules, nil
}

type MemoryPermissionRuleStore struct {
	mu    sync.RWMutex
	rules *rule.PermissionRules
}

func NewMemoryPermissionRuleStore() *MemoryPermissionRuleStore {
	return &MemoryPermissionRuleStore{rules: &rule.PermissionRules{}}
}

func (s *MemoryPermissionRuleStore) Load() (*rule.PermissionRules, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rules == nil {
		return &rule.PermissionRules{}, nil
	}
	return s.rules, nil
}

func (s *MemoryPermissionRuleStore) Save(rules *rule.PermissionRules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
	return nil
}

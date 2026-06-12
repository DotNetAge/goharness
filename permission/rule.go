// Package permission provides a flexible permission checking system for tool usage.
// It supports rule-based permission matching, skill-based access control,
// and chainable permission checkers that can be composed together.
//
// The permission system follows a deny-by-default approach where tools are
// allowed unless explicitly denied by matching rules. Rules can be configured
// to always allow, always deny, or require approval (ask) for specific tool
// operations based on tool name and content patterns.
package permission

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/rule"
	"github.com/DotNetAge/goharness/tools"
)

// MatchResult represents the outcome of a permission rule matching operation.
// It contains information about whether a rule matched, the behavior that
// should be applied, the matched rule itself, and a human-readable message.
type MatchResult struct {
	Matched  bool
	Behavior rule.RuleBehavior
	Rule     *rule.PermissionRule
	Message  string
}

// RuleMatcher matches tool usage against a set of permission rules.
// It evaluates rules in priority order: AlwaysDeny > AlwaysAllow > AlwaysAsk.
// The first matching rule determines the permission outcome.
type RuleMatcher struct {
	rules *rule.PermissionRules
}

// NewRuleMatcher creates a new RuleMatcher with the given permission rules.
func NewRuleMatcher(rules *rule.PermissionRules) *RuleMatcher {
	return &RuleMatcher{rules: rules}
}

// Match checks if a tool usage matches any permission rule.
// It returns a MatchResult containing the match status and behavior.
// Rules are evaluated in order: deny rules first, then allow, then ask.
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

// ruleMatches checks if a single permission rule matches the given tool usage.
// It matches on tool name (supporting wildcards) and optionally on content patterns.
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

// extractContentParam extracts the relevant content parameter from tool params
// for pattern matching. It checks common parameter names like command,
// file_path, path, filePath, and url.
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

// matchContent checks if content matches a pattern.
// Patterns can use glob wildcards (*, ?) or simple substring/prefix matching.
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

// buildRuleMessage creates a human-readable message for a matched rule.
// It includes the action (allowed/denied/requires approval) and rule details.
func buildRuleMessage(r *rule.PermissionRule, action string) string {
	if r.Description != "" {
		return fmt.Sprintf("rule %s: %s", action, r.Description)
	}
	if r.ContentPattern != "" {
		return fmt.Sprintf("rule %s: %s(%s)", action, r.ToolName, r.ContentPattern)
	}
	return fmt.Sprintf("rule %s: %s", action, r.ToolName)
}

// RuleBasedChecker implements permission checking using a rule store.
// It caches loaded rules and supports refreshing the cache.
// Thread-safe with read-write locking for concurrent access.
type RuleBasedChecker struct {
	store rule.PermissionRuleStore
	mu    sync.RWMutex
	rules *rule.PermissionRules
}

// NewRuleBasedChecker creates a new RuleBasedChecker that loads rules from the given store.
func NewRuleBasedChecker(store rule.PermissionRuleStore) *RuleBasedChecker {
	return &RuleBasedChecker{store: store}
}

// CheckPermissions checks if a tool usage is permitted based on loaded rules.
// Returns PermissionAllow if no rules match or if rule loading fails (fail-open).
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

// Refresh clears the cached rules, forcing a reload on next check.
func (c *RuleBasedChecker) Refresh() {
	c.mu.Lock()
	c.rules = nil
	c.mu.Unlock()
}

// getRules loads rules from the store with caching.
// Uses double-checked locking for thread-safe lazy initialization.
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

// MemoryPermissionRuleStore is an in-memory implementation of PermissionRuleStore.
// Useful for testing or when rules are loaded from external sources and cached.
// Thread-safe with read-write locking for concurrent access.
type MemoryPermissionRuleStore struct {
	mu    sync.RWMutex
	rules *rule.PermissionRules
}

// NewMemoryPermissionRuleStore creates a new empty in-memory rule store.
func NewMemoryPermissionRuleStore() *MemoryPermissionRuleStore {
	return &MemoryPermissionRuleStore{rules: &rule.PermissionRules{}}
}

// Load returns the currently stored permission rules.
func (s *MemoryPermissionRuleStore) Load() (*rule.PermissionRules, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rules == nil {
		return &rule.PermissionRules{}, nil
	}
	return s.rules, nil
}

// Save stores the given permission rules, replacing any existing rules.
func (s *MemoryPermissionRuleStore) Save(rules *rule.PermissionRules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
	return nil
}

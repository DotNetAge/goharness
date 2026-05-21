package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ── RuleBehavior ───────────────────────────────────────────────────────────

// RuleBehavior defines the action taken when a permission rule matches.
type RuleBehavior string

const (
	RuleAllow RuleBehavior = "allow"
	RuleDeny  RuleBehavior = "deny"
	RuleAsk   RuleBehavior = "ask"
)

// ── Data types ─────────────────────────────────────────────────────────────

// PermissionRule defines a single permission rule.
type PermissionRule struct {
	// Behavior is the action when this rule matches: allow/deny/ask.
	Behavior RuleBehavior `json:"behavior"`
	// ToolName is the tool name to match (e.g. "Bash", "Write").
	// Required. Use "*" to match all tools.
	ToolName string `json:"tool_name"`
	// ContentPattern is an optional content/argument pattern.
	//   - Bash/PowerShell: command prefix match (e.g. "git ")
	//   - Write/FileEdit: path glob (e.g. "/src/**")
	//   - Read/Grep/Glob/Ls: path glob (e.g. "/etc/**")
	//   - WebFetch: URL prefix match (e.g. "https://api.example.com")
	//   - Empty: matches any call to the tool (no content restriction)
	ContentPattern string `json:"content_pattern,omitempty"`
	// Description explains why this rule exists (for UI display).
	Description string `json:"description,omitempty"`
	// Source identifies where this rule came from (e.g. "user", "policy").
	Source string `json:"source,omitempty"`
}

// PermissionRules holds the complete set of permission rules organized by behavior.
type PermissionRules struct {
	AlwaysAllow []PermissionRule `json:"always_allow"`
	AlwaysDeny  []PermissionRule `json:"always_deny"`
	AlwaysAsk   []PermissionRule `json:"always_ask"`
}

// ── Rule store interface ───────────────────────────────────────────────────

// PermissionRuleStore is the persistence interface for permission rules.
// Implementations can be file-backed, DB-backed, etc.
// External code injects the implementation; goreact provides MemoryPermissionRuleStore as default.
type PermissionRuleStore interface {
	// Load returns the current rules. Never returns nil — returns empty PermissionRules on first call.
	Load() (*PermissionRules, error)
	// Save persists the rules. The store decides where/how to store.
	Save(rules *PermissionRules) error
}

// MemoryPermissionRuleStore is the default in-memory implementation of PermissionRuleStore.
// Rules are lost on restart — intended as a fallback when no external store is injected.
type MemoryPermissionRuleStore struct {
	mu    sync.RWMutex
	rules *PermissionRules
}

func NewMemoryPermissionRuleStore() *MemoryPermissionRuleStore {
	return &MemoryPermissionRuleStore{
		rules: &PermissionRules{},
	}
}

func (s *MemoryPermissionRuleStore) Load() (*PermissionRules, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rules == nil {
		return &PermissionRules{}, nil
	}
	return s.rules, nil
}

func (s *MemoryPermissionRuleStore) Save(rules *PermissionRules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
	return nil
}

// ── Matching ───────────────────────────────────────────────────────────────

// MatchResult holds the result of matching rules against a tool call.
type MatchResult struct {
	Matched  bool
	Behavior RuleBehavior
	Rule     *PermissionRule
	Message  string
}

// RuleMatcher matches rules against tool calls.
type RuleMatcher struct {
	rules *PermissionRules
}

func NewRuleMatcher(rules *PermissionRules) *RuleMatcher {
	return &RuleMatcher{rules: rules}
}

// Match returns the first matching rule, checking deny → allow → ask.
// Deny is checked first (safety first: deny rules take precedence).
// Returns {Matched: false} when no rule matches.
func (m *RuleMatcher) Match(toolName string, params map[string]any) MatchResult {
	rules := m.rules
	if rules == nil {
		return MatchResult{}
	}

	// 1. Deny rules (safety first)
	for _, r := range rules.AlwaysDeny {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: RuleDeny,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "denied"),
			}
		}
	}

	// 2. Allow rules
	for _, r := range rules.AlwaysAllow {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: RuleAllow,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "allowed"),
			}
		}
	}

	// 3. Ask rules
	for _, r := range rules.AlwaysAsk {
		if ruleMatches(&r, toolName, params) {
			return MatchResult{
				Matched:  true,
				Behavior: RuleAsk,
				Rule:     &r,
				Message:  buildRuleMessage(&r, "requires approval"),
			}
		}
	}

	return MatchResult{}
}

// ruleMatches checks if a single rule matches a tool call.
func ruleMatches(r *PermissionRule, toolName string, params map[string]any) bool {
	// Tool name match
	if r.ToolName != "" && r.ToolName != "*" && r.ToolName != toolName {
		return false
	}

	// No content pattern → matches any call to this tool
	if r.ContentPattern == "" {
		return true
	}

	// Content pattern matching — try common parameter keys
	content := extractContentParam(toolName, params)
	if content == "" {
		return false
	}

	return matchContent(r.ContentPattern, content)
}

// extractContentParam gets the primary content parameter for a tool.
func extractContentParam(toolName string, params map[string]any) string {
	// Command-based tools
	if cmd, ok := params["command"]; ok {
		if s, ok := cmd.(string); ok {
			return s
		}
	}

	// Path-based tools — try multiple param names
	for _, key := range []string{"file_path", "path", "filePath"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}

	// URL-based tools
	if url, ok := params["url"]; ok {
		if s, ok := url.(string); ok {
			return s
		}
	}

	return ""
}

// matchContent checks if content matches the pattern.
//   - Path patterns: if pattern contains "*", checks prefix before the wildcard
//     (so "/home/*" matches "/home/user/file.txt").
//     Otherwise uses filepath.Match for exact glob matching.
//   - Command/URL patterns: uses strings.HasPrefix
//   - Fallback: substring match
func matchContent(pattern, content string) bool {
	// If pattern contains a wildcard, check the prefix before it
	if strings.Contains(pattern, "*") || strings.Contains(pattern, "?") {
		prefix := strings.SplitN(pattern, "*", 2)[0]
		prefix = strings.SplitN(prefix, "?", 2)[0]
		if prefix != "" && strings.HasPrefix(content, prefix) {
			return true
		}
	}

	// Try exact glob match (for single-segment patterns like "*.go")
	if matched, err := filepath.Match(pattern, content); err == nil && matched {
		return true
	}

	// Try prefix match (for commands, URLs)
	if strings.HasPrefix(content, pattern) {
		return true
	}

	// Try substring match (for partial patterns)
	if strings.Contains(content, pattern) {
		return true
	}

	return false
}

func buildRuleMessage(r *PermissionRule, action string) string {
	if r.Description != "" {
		return fmt.Sprintf("rule %s: %s", action, r.Description)
	}
	if r.ContentPattern != "" {
		return fmt.Sprintf("rule %s: %s(%s)", action, r.ToolName, r.ContentPattern)
	}
	return fmt.Sprintf("rule %s: %s", action, r.ToolName)
}

// ── ToolPermissionChecker implementation ───────────────────────────────────

// RuleBasedChecker implements ToolPermissionChecker by matching permission rules.
// Add it to the PermissionChain before AskPermission to check rules first.
type RuleBasedChecker struct {
	store PermissionRuleStore

	mu    sync.RWMutex
	rules *PermissionRules // cached rules
}

func NewRuleBasedChecker(store PermissionRuleStore) *RuleBasedChecker {
	return &RuleBasedChecker{store: store}
}

// CheckPermissions implements ToolPermissionChecker.
// Returns:
//   - PermissionAllow + empty → no rule matched (pass through to next checker)
//   - PermissionDeny          → denied by rule
//   - PermissionAllow + msg  → allowed by rule
//   - PermissionAsk          → requires approval by rule
func (c *RuleBasedChecker) CheckPermissions(ctx *ToolUseContext) PermissionResult {
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
	case RuleDeny:
		return PermissionResult{
			Behavior: PermissionDeny,
			Message:  match.Message,
		}
	case RuleAllow:
		return PermissionResult{
			Behavior: PermissionAllow,
			Message:  match.Message,
		}
	case RuleAsk:
		return PermissionResult{
			Behavior: PermissionAsk,
			Message:  match.Message,
		}
	default:
		return PermissionResult{Behavior: PermissionAllow}
	}
}

// Refresh forces re-loading rules from the store on the next check.
func (c *RuleBasedChecker) Refresh() {
	c.mu.Lock()
	c.rules = nil
	c.mu.Unlock()
}

func (c *RuleBasedChecker) getRules() (*PermissionRules, error) {
	c.mu.RLock()
	if c.rules != nil {
		c.mu.RUnlock()
		return c.rules, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
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

// Package rule provides types and interfaces for permission and behavior rules.
// It includes permission rule definitions for tool access control,
// as well as behavior rule types for AI agent constraint management.
package rule

// RuleBehavior defines the possible behaviors when a permission rule matches.
type RuleBehavior string

const (
	// RuleAllow indicates the tool usage is permitted.
	RuleAllow RuleBehavior = "allow"

	// RuleDeny indicates the tool usage is forbidden.
	RuleDeny RuleBehavior = "deny"

	// RuleAsk indicates the tool usage requires user approval.
	RuleAsk RuleBehavior = "ask"
)

// PermissionRule defines a single permission rule for tool access control.
type PermissionRule struct {
	Behavior       RuleBehavior `json:"behavior"`
	ToolName       string       `json:"tool_name"`
	ContentPattern string       `json:"content_pattern,omitempty"`
	Description    string       `json:"description,omitempty"`
	Source         string       `json:"source,omitempty"`
}

// PermissionRules holds categorized permission rules organized by behavior type.
type PermissionRules struct {
	AlwaysAllow []PermissionRule `json:"always_allow"`
	AlwaysDeny  []PermissionRule `json:"always_deny"`
	AlwaysAsk   []PermissionRule `json:"always_ask"`
}

// PermissionRuleStore defines the interface for loading and saving permission rules.
type PermissionRuleStore interface {
	// Load retrieves the current set of permission rules.
	Load() (*PermissionRules, error)
	// Save persists the given permission rules.
	Save(rules *PermissionRules) error
}

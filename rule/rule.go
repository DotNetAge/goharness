package rule

type RuleBehavior string

const (
	RuleAllow RuleBehavior = "allow"
	RuleDeny  RuleBehavior = "deny"
	RuleAsk   RuleBehavior = "ask"
)

type PermissionRule struct {
	Behavior       RuleBehavior `json:"behavior"`
	ToolName       string       `json:"tool_name"`
	ContentPattern string       `json:"content_pattern,omitempty"`
	Description    string       `json:"description,omitempty"`
	Source         string       `json:"source,omitempty"`
}

type PermissionRules struct {
	AlwaysAllow []PermissionRule `json:"always_allow"`
	AlwaysDeny  []PermissionRule `json:"always_deny"`
	AlwaysAsk   []PermissionRule `json:"always_ask"`
}

type PermissionRuleStore interface {
	Load() (*PermissionRules, error)
	Save(rules *PermissionRules) error
}

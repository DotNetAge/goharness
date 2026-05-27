package permission

import (
	"testing"

	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/tools"
)

func TestRuleMatcher_Match_NoRules(t *testing.T) {
	matcher := NewRuleMatcher(nil)
	result := matcher.Match("bash", map[string]any{"command": "ls"})

	if result.Matched {
		t.Errorf("expected no match with nil rules, got matched")
	}
}

func TestRuleMatcher_Match_EmptyRules(t *testing.T) {
	rules := &rule.PermissionRules{}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("bash", map[string]any{"command": "ls"})

	if result.Matched {
		t.Errorf("expected no match with empty rules, got matched")
	}
}

func TestRuleMatcher_Match_DenyRule(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "bash",
				ContentPattern: "rm -rf",
				Description:    "No destructive commands",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("bash", map[string]any{"command": "rm -rf /"})

	if !result.Matched {
		t.Fatalf("expected match for deny rule, got no match")
	}
	if result.Behavior != rule.RuleDeny {
		t.Errorf("expected behavior %q, got %q", rule.RuleDeny, result.Behavior)
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestRuleMatcher_Match_AllowRule(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{
				Behavior:    rule.RuleAllow,
				ToolName:    "read_file",
				Description: "Allow reading files",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("read_file", nil)

	if !result.Matched {
		t.Fatalf("expected match for allow rule, got no match")
	}
	if result.Behavior != rule.RuleAllow {
		t.Errorf("expected behavior %q, got %q", rule.RuleAllow, result.Behavior)
	}
}

func TestRuleMatcher_Match_AskRule(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAsk: []rule.PermissionRule{
			{
				Behavior:       rule.RuleAsk,
				ToolName:       "write_file",
				ContentPattern: "*.go",
				Description:    "Ask before writing Go files",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("write_file", map[string]any{"file_path": "main.go"})

	if !result.Matched {
		t.Fatalf("expected match for ask rule, got no match")
	}
	if result.Behavior != rule.RuleAsk {
		t.Errorf("expected behavior %q, got %q", rule.RuleAsk, result.Behavior)
	}
}

func TestRuleMatcher_Match_WildcardToolName(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "*",
				ContentPattern: "/etc/passwd",
				Description:    "No access to passwd",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("read_file", map[string]any{"path": "/etc/passwd"})

	if !result.Matched {
		t.Fatalf("expected match with wildcard tool name, got no match")
	}
	if result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny behavior, got %q", result.Behavior)
	}
}

func TestRuleMatcher_Match_DenyTakesPriority(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "bash",
				ContentPattern: "rm*",
			},
		},
		AlwaysAllow: []rule.PermissionRule{
			{
				Behavior:    rule.RuleAllow,
				ToolName:    "bash",
				Description: "Allow bash",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("bash", map[string]any{"command": "rm file.txt"})

	if !result.Matched {
		t.Fatalf("expected match, got no match")
	}
	if result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny to take priority over allow, got %q", result.Behavior)
	}
}

func TestRuleMatcher_Match_ContentPatternPrefix(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "write_file",
				ContentPattern: "/tmp/",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("write_file", map[string]any{"path": "/tmp/test.txt"})

	if !result.Matched {
		t.Fatalf("expected match with prefix pattern, got no match")
	}
}

func TestRuleMatcher_Match_ContentPatternSubstring(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "bash",
				ContentPattern: "curl",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("bash", map[string]any{"command": "curl http://example.com"})

	if !result.Matched {
		t.Fatalf("expected match with substring pattern, got no match")
	}
}

func TestRuleMatcher_Match_NoMatchWrongTool(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior: rule.RuleDeny,
				ToolName: "bash",
			},
		},
	}
	matcher := NewRuleMatcher(rules)
	result := matcher.Match("read_file", nil)

	if result.Matched {
		t.Errorf("expected no match for different tool name, got match")
	}
}

func TestRuleMatcher_Match_ExtractContentParam(t *testing.T) {
	testCases := []struct {
		name     string
		params   map[string]any
		expected string
	}{
		{
			name:     "command param",
			params:   map[string]any{"command": "ls -la"},
			expected: "ls -la",
		},
		{
			name:     "file_path param",
			params:   map[string]any{"file_path": "/tmp/file.txt"},
			expected: "/tmp/file.txt",
		},
		{
			name:     "path param",
			params:   map[string]any{"path": "/home/user/doc"},
			expected: "/home/user/doc",
		},
		{
			name:     "filePath param",
			params:   map[string]any{"filePath": "config.yaml"},
			expected: "config.yaml",
		},
		{
			name:     "url param",
			params:   map[string]any{"url": "https://example.com"},
			expected: "https://example.com",
		},
		{
			name:     "no content params",
			params:   map[string]any{"other": "value"},
			expected: "",
		},
		{
			name:     "empty params",
			params:   nil,
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			content := extractContentParam("test_tool", tc.params)
			if content != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, content)
			}
		})
	}
}

func TestMatchContent_GlobPatterns(t *testing.T) {
	testCases := []struct {
		name     string
		pattern  string
		content  string
		expected bool
	}{
		{
			name:     "glob wildcard",
			pattern:  "*.go",
			content:  "main.go",
			expected: true,
		},
		{
			name:     "glob question mark",
			pattern:  "file?.txt",
			content:  "file1.txt",
			expected: true,
		},
		{
			name:     "prefix match",
			pattern:  "/home/",
			content:  "/home/user/file.txt",
			expected: true,
		},
		{
			name:     "substring match",
			pattern:  "secret",
			content:  "/path/to/secret/config.yaml",
			expected: true,
		},
		{
			name:     "no match",
			pattern:  "admin",
			content:  "/public/page.html",
			expected: false,
		},
		{
			name:     "empty pattern",
			pattern:  "",
			content:  "anything",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := matchContent(tc.pattern, tc.content)
			if result != tc.expected {
				t.Errorf("pattern %q matching %q: expected %v, got %v",
					tc.pattern, tc.content, tc.expected, result)
			}
		})
	}
}

func TestBuildRuleMessage(t *testing.T) {
	testCases := []struct {
		name     string
		rule     rule.PermissionRule
		action   string
		contains string
	}{
		{
			name: "with description",
			rule: rule.PermissionRule{
				ToolName:    "bash",
				Description: "No destructive commands",
			},
			action:   "denied",
			contains: "No destructive commands",
		},
		{
			name: "with content pattern",
			rule: rule.PermissionRule{
				ToolName:       "write_file",
				ContentPattern: "/etc/*",
			},
			action:   "denied",
			contains: "write_file(/etc/*)",
		},
		{
			name: "tool name only",
			rule: rule.PermissionRule{
				ToolName: "read_file",
			},
			action:   "allowed",
			contains: "read_file",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			msg := buildRuleMessage(&tc.rule, tc.action)
			if !contains(msg, tc.contains) {
				t.Errorf("message %q should contain %q", msg, tc.contains)
			}
		})
	}
}

func TestNewMemoryPermissionRuleStore(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	if store == nil {
		t.Fatal("expected non-nil store")
	}

	rules, err := store.Load()
	if err != nil {
		t.Fatalf("unexpected error loading rules: %v", err)
	}
	if rules == nil {
		t.Fatal("expected non-nil rules")
	}
}

func TestMemoryPermissionRuleStore_SaveAndLoad(t *testing.T) {
	store := NewMemoryPermissionRuleStore()

	expectedRules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior: rule.RuleDeny,
				ToolName: "dangerous_tool",
			},
		},
	}

	err := store.Save(expectedRules)
	if err != nil {
		t.Fatalf("unexpected error saving rules: %v", err)
	}

	loadedRules, err := store.Load()
	if err != nil {
		t.Fatalf("unexpected error loading rules: %v", err)
	}

	if len(loadedRules.AlwaysDeny) != len(expectedRules.AlwaysDeny) {
		t.Errorf("expected %d deny rules, got %d",
			len(expectedRules.AlwaysDeny), len(loadedRules.AlwaysDeny))
	}
}

func TestRuleBasedChecker_CheckPermissions_NoRules(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{
		ToolName: "bash",
		Params:   map[string]any{"command": "ls"},
	}

	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Errorf("expected allow when no rules, got %s", result.Behavior)
	}
}

func TestRuleBasedChecker_CheckPermissions_WithDenyRule(t *testing.T) {
	store := NewMemoryPermissionRuleStore()

	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior:       rule.RuleDeny,
				ToolName:       "bash",
				ContentPattern: "rm*",
			},
		},
	}
	store.Save(rules)

	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{
		ToolName: "bash",
		Params:   map[string]any{"command": "rm file.txt"},
	}

	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionDeny {
		t.Errorf("expected deny, got %s", result.Behavior)
	}
	if result.Message == "" {
		t.Error("expected non-empty message on deny")
	}
}

func TestRuleBasedChecker_Refresh(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{
		ToolName: "bash",
		Params:   map[string]any{"command": "ls"},
	}

	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Errorf("expected allow initially, got %s", result.Behavior)
	}

	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{
				Behavior: rule.RuleDeny,
				ToolName: "*",
			},
		},
	}
	store.Save(rules)

	result = checker.CheckPermissions(ctx)
	if result.Behavior == PermissionDeny {
		t.Log("rules cached, still allowing (expected before refresh)")
	}

	checker.Refresh()

	result = checker.CheckPermissions(ctx)
	if result.Behavior != PermissionDeny {
		t.Errorf("expected deny after refresh, got %s", result.Behavior)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && search(s, substr)
}

func search(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

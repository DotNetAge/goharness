package permission

import (
	"testing"

	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/tools"
)

func TestRuleMatcher_NoRules(t *testing.T) {
	matcher := NewRuleMatcher(&rule.PermissionRules{})
	result := matcher.Match("Bash", map[string]any{"command": "rm -rf /"})
	if result.Matched {
		t.Errorf("expected no match with empty rules, got matched=%v behavior=%v", result.Matched, result.Behavior)
	}
}

func TestRuleMatcher_NilRules(t *testing.T) {
	matcher := NewRuleMatcher(nil)
	result := matcher.Match("Bash", nil)
	if result.Matched {
		t.Error("expected no match with nil rules")
	}
}

func TestRuleMatcher_ToolNameAllow(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Read", Behavior: rule.RuleAllow, Description: "allow reads"},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Read", nil)
	if !result.Matched {
		t.Error("expected match for Read")
	}
	if result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow, got %v", result.Behavior)
	}

	result = matcher.Match("Bash", nil)
	if result.Matched {
		t.Error("expected no match for Bash when only Read rule exists")
	}
}

func TestRuleMatcher_WildcardToolName(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "*", Behavior: rule.RuleDeny},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Bash", nil)
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny for all tools with wildcard")
	}

	result = matcher.Match("Read", nil)
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny for all tools with wildcard")
	}
}

func TestRuleMatcher_DenyPriority(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "Bash", Behavior: rule.RuleDeny, Description: "no bash"},
		},
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Bash", Behavior: rule.RuleAllow, Description: "allow bash"},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Bash", nil)
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny (deny takes priority), got %v", result.Behavior)
	}
}

func TestRuleMatcher_ContentPatternCommand(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Bash", ContentPattern: "git ", Behavior: rule.RuleAllow},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Bash", map[string]any{"command": "git status"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for git command, got %v", result.Behavior)
	}

	result = matcher.Match("Bash", map[string]any{"command": "rm -rf /"})
	if result.Matched {
		t.Errorf("expected no match for non-git command")
	}
}

func TestRuleMatcher_ContentPatternPath(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "Write", ContentPattern: "/etc/*", Behavior: rule.RuleDeny},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Write", map[string]any{"file_path": "/etc/passwd"})
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny for /etc/passwd")
	}

	result = matcher.Match("Write", map[string]any{"file_path": "/home/user/file.txt"})
	if result.Matched {
		t.Errorf("expected no match for non-/etc path")
	}
}

func TestRuleMatcher_DenyOverridesAllow(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "Bash", ContentPattern: "rm ", Behavior: rule.RuleDeny},
		},
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Bash", ContentPattern: "git ", Behavior: rule.RuleAllow},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Bash", map[string]any{"command": "rm -rf /"})
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny for rm command, got %v", result.Behavior)
	}

	result = matcher.Match("Bash", map[string]any{"command": "git status"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for git command, got %v", result.Behavior)
	}

	result = matcher.Match("Bash", map[string]any{"command": "ls -la"})
	if result.Matched {
		t.Errorf("expected no match for unmatched command")
	}
}

func TestRuleMatcher_ContentPatternURL(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "WebFetch", ContentPattern: "https://api.example.com", Behavior: rule.RuleAllow},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("WebFetch", map[string]any{"url": "https://api.example.com/v1/users"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for matching URL prefix")
	}

	result = matcher.Match("WebFetch", map[string]any{"url": "https://evil.com"})
	if result.Matched {
		t.Errorf("expected no match for non-matching URL")
	}
}

func TestRuleMatcher_ToolNameOnlyMatch(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Glob", Behavior: rule.RuleAllow},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Glob", map[string]any{"pattern": "**/*.go"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for Glob with no content pattern")
	}
}

func TestMemoryPermissionRuleStore_DefaultEmpty(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	rules, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(rules.AlwaysAllow)+len(rules.AlwaysDeny)+len(rules.AlwaysAsk) != 0 {
		t.Error("expected empty rules from default store")
	}
}

func TestMemoryPermissionRuleStore_SaveAndLoad(t *testing.T) {
	store := NewMemoryPermissionRuleStore()

	saved := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Read", Behavior: rule.RuleAllow},
		},
	}
	if err := store.Save(saved); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(loaded.AlwaysAllow) != 1 || loaded.AlwaysAllow[0].ToolName != "Read" {
		t.Errorf("expected 1 allow rule for Read, got %+v", loaded.AlwaysAllow)
	}
}

func TestRuleBasedChecker_NoStoreRulePassThrough(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{ToolName: "Bash", Params: map[string]any{"command": "ls"}}
	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Errorf("expected Allow (pass through) with no rules, got %v", result.Behavior)
	}
}

func TestRuleBasedChecker_DenyByRule(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	store.Save(&rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "Bash", ContentPattern: "rm ", Behavior: rule.RuleDeny},
		},
	})
	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{ToolName: "Bash", Params: map[string]any{"command": "rm -rf /"}}
	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionDeny {
		t.Errorf("expected Deny for rm command, got %v", result.Behavior)
	}
}

func TestRuleBasedChecker_RefreshAfterStoreChange(t *testing.T) {
	store := NewMemoryPermissionRuleStore()
	checker := NewRuleBasedChecker(store)

	ctx := &tools.ToolUseContext{ToolName: "Bash", Params: map[string]any{"command": "rm -rf /"}}
	result := checker.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Fatalf("expected Allow (no rules yet), got %v", result.Behavior)
	}

	store.Save(&rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "Bash", ContentPattern: "rm ", Behavior: rule.RuleDeny},
		},
	})

	result = checker.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Fatalf("expected Allow (stale cache), got %v", result.Behavior)
	}

	checker.Refresh()
	result = checker.CheckPermissions(ctx)
	if result.Behavior != PermissionDeny {
		t.Errorf("expected Deny after Refresh, got %v", result.Behavior)
	}
}

func TestRuleMatcher_FileEditPathMatch(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysDeny: []rule.PermissionRule{
			{ToolName: "FileEdit", ContentPattern: "/etc/*", Behavior: rule.RuleDeny},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("FileEdit", map[string]any{"file_path": "/etc/hosts"})
	if !result.Matched || result.Behavior != rule.RuleDeny {
		t.Errorf("expected deny for /etc/hosts FileEdit")
	}
}

func TestRuleMatcher_MultipleContentParamKeys(t *testing.T) {
	rules := &rule.PermissionRules{
		AlwaysAllow: []rule.PermissionRule{
			{ToolName: "Read", ContentPattern: "/home/*", Behavior: rule.RuleAllow},
		},
	}
	matcher := NewRuleMatcher(rules)

	result := matcher.Match("Read", map[string]any{"file_path": "/home/user/file.txt"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for file_path key")
	}

	result = matcher.Match("Read", map[string]any{"path": "/home/user/file.txt"})
	if !result.Matched || result.Behavior != rule.RuleAllow {
		t.Errorf("expected allow for path key")
	}
}

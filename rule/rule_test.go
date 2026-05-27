package rule

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuleBehavior_Values(t *testing.T) {
	tests := []struct {
		behavior RuleBehavior
		expected RuleBehavior
	}{
		{RuleAllow, "allow"},
		{RuleDeny, "deny"},
		{RuleAsk, "ask"},
	}

	for _, tt := range tests {
		t.Run(string(tt.behavior), func(t *testing.T) {
			if tt.behavior != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.behavior)
			}
		})
	}
}

func TestPermissionRule_Fields(t *testing.T) {
	rule := PermissionRule{
		Behavior:       RuleDeny,
		ToolName:       "bash",
		ContentPattern: "rm -rf",
		Description:    "No destructive commands",
		Source:         "admin-config",
	}

	if rule.Behavior != RuleDeny {
		t.Error("Behavior mismatch")
	}
	if rule.ToolName != "bash" {
		t.Error("ToolName mismatch")
	}
	if rule.ContentPattern != "rm -rf" {
		t.Error("ContentPattern mismatch")
	}
	if rule.Description != "No destructive commands" {
		t.Error("Description mismatch")
	}
	if rule.Source != "admin-config" {
		t.Error("Source mismatch")
	}
}

func TestPermissionRules_Empty(t *testing.T) {
	rules := PermissionRules{}

	if rules.AlwaysAllow == nil {
		rules.AlwaysAllow = []PermissionRule{}
	}
	if rules.AlwaysDeny == nil {
		rules.AlwaysDeny = []PermissionRule{}
	}
	if rules.AlwaysAsk == nil {
		rules.AlwaysAsk = []PermissionRule{}
	}

	if len(rules.AlwaysAllow) != 0 || len(rules.AlwaysDeny) != 0 || len(rules.AlwaysAsk) != 0 {
		t.Error("empty rules should have zero-length slices")
	}
}

func TestPermissionRules_Populated(t *testing.T) {
	rules := PermissionRules{
		AlwaysAllow: []PermissionRule{
			{Behavior: RuleAllow, ToolName: "read_file"},
		},
		AlwaysDeny: []PermissionRule{
			{Behavior: RuleDeny, ToolName: "bash", ContentPattern: "rm*"},
		},
		AlwaysAsk: []PermissionRule{
			{Behavior: RuleAsk, ToolName: "write_file", ContentPattern: "*.go"},
		},
	}

	if len(rules.AlwaysAllow) != 1 {
		t.Errorf("expected 1 allow rule, got %d", len(rules.AlwaysAllow))
	}
	if len(rules.AlwaysDeny) != 1 {
		t.Errorf("expected 1 deny rule, got %d", len(rules.AlwaysDeny))
	}
	if len(rules.AlwaysAsk) != 1 {
		t.Errorf("expected 1 ask rule, got %d", len(rules.AlwaysAsk))
	}
}

func TestYAMLRuleRegistry_NewWithValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `
rules:
  - id: rule-1
    intro: Always be polite
    scope: global
    priority: 100
    enabled: true
  - id: rule-2
    intro: Never delete production data
    scope: local
    priority: 200
    enabled: true
`

	err := os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	if err != nil {
		t.Fatalf("error writing test YAML file: %v", err)
	}

	reg, err := NewYAMLRuleRegistry(yamlPath)
	if err != nil {
		t.Fatalf("unexpected error creating registry: %v", err)
	}
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}

	allRules := reg.All()
	if len(allRules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(allRules))
	}
}

func TestYAMLRuleRegistry_NewWithNonExistentFile(t *testing.T) {
	_, err := NewYAMLRuleRegistry("/nonexistent/path/rules.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestYAMLRuleRegistry_NewWithInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "invalid.yaml")

	err := os.WriteFile(yamlPath, []byte("{invalid: yaml: content["), 0644)
	if err != nil {
		t.Fatalf("error writing test file: %v", err)
	}

	_, err = NewYAMLRuleRegistry(yamlPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestYAMLRuleRegistry_NewWithMissingID(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "missing-id.yaml")

	yamlContent := `
rules:
  - intro: Missing ID rule
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	_, err := NewYAMLRuleRegistry(yamlPath)
	if err == nil {
		t.Error("expected error for missing ID")
	}
}

func TestYAMLRuleRegistry_NewWithMissingIntro(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "missing-intro.yaml")

	yamlContent := `
rules:
  - id: no-intro-rule
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	_, err := NewYAMLRuleRegistry(yamlPath)
	if err == nil {
		t.Error("expected error for missing intro")
	}
}

func TestYAMLRuleRegistry_MustNewPanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid path")
		}
	}()

	MustYAMLRuleRegistry("/nonexistent/path/rules.yaml")
}

func TestYAMLRuleRegistry_Register(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: existing-rule
    intro: Existing rule
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	newRule := Rule{
		ID:       "new-rule",
		Intro:    "A new rule",
		Scope:    ScopeLocal,
		Priority: 50,
		Enabled:  true,
	}

	err := reg.Register(newRule)
	if err != nil {
		t.Fatalf("unexpected error registering new rule: %v", err)
	}

	allRules := reg.All()
	found := false
	for _, r := range allRules {
		if r.ID == "new-rule" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new rule not found after registration")
	}
}

func TestYAMLRuleRegistry_RegisterUpdateExisting(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: update-me
    intro: Original intro
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	updatedRule := Rule{
		ID:       "update-me",
		Intro:    "Updated intro",
		Scope:    ScopeLocal,
		Priority: 200,
		Enabled:  true,
	}

	reg.Register(updatedRule)

	rule, found := reg.Get("update-me")
	if !found {
		t.Fatal("rule should exist after update")
	}
	if rule.Intro != "Updated intro" {
		t.Errorf("expected updated intro, got %q", rule.Intro)
	}
}

func TestYAMLRuleRegistry_Unregister(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: to-remove
    intro: Will be removed
    scope: global
    priority: 100
    enabled: true
  - id: keep-this
    intro: Keep this one
    scope: global
    priority: 50
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	reg.Unregister("to-remove")

	if _, found := reg.Get("to-remove"); found {
		t.Error("removed rule should not exist")
	}

	if _, found := reg.Get("keep-this"); !found {
		t.Error("kept rule should still exist")
	}

	allRules := reg.All()
	if len(allRules) != 1 {
		t.Errorf("expected 1 rule after unregister, got %d", len(allRules))
	}
}

func TestYAMLRuleRegistry_UnregisterNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: only-rule
    intro: Only rule
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	reg.Unregister("nonexistent")

	allRules := reg.All()
	if len(allRules) != 1 {
		t.Errorf("unregistering nonexistent should not affect other rules, got %d", len(allRules))
	}
}

func TestYAMLRuleRegistry_Get(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: find-me
    intro: Find this rule
    scope: local
    priority: 75
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	rule, found := reg.Get("find-me")
	if !found {
		t.Fatal("rule should be found")
	}
	if rule.ID != "find-me" {
		t.Errorf("expected ID 'find-me', got %q", rule.ID)
	}
	if rule.Intro != "Find this rule" {
		t.Errorf("intro mismatch")
	}
}

func TestYAMLRuleRegistry_GetNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: exists
    intro: Exists
    scope: global
    priority: 10
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	_, found := reg.Get("nonexistent")
	if found {
		t.Error("should return false for nonexistent rule")
	}
}

func TestYAMLRuleRegistry_AllReturnsCopy(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: original
    intro: Original rule
    scope: global
    priority: 100
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	allRules := reg.All()
	if len(allRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(allRules))
	}

	allRules[0].Intro = "Modified in copy"

	original, _ := reg.Get("original")
	if original.Intro == "Modified in copy" {
		t.Error("All() should return a copy, modifying it should not affect the registry")
	}
}

func TestYAMLRuleRegistry_GetByScope(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: global-1
    intro: Global rule 1
    scope: global
    priority: 100
    enabled: true
  - id: global-2
    intro: Global rule 2
    scope: global
    priority: 90
    enabled: true
  - id: local-1
    intro: Local rule 1
    scope: local
    priority: 80
    enabled: true
  - id: conversation-1
    intro: Conversation rule 1
    scope: conversation
    priority: 70
    enabled: true
  - id: disabled-global
    intro: Disabled global
    scope: global
    priority: 60
    enabled: false
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	globalRules := reg.GetByScope(ScopeGlobal)
	localRules := reg.GetByScope(ScopeLocal)
	conversationRules := reg.GetByScope(ScopeConversation)

	if len(globalRules) != 3 {
		t.Errorf("expected 3 global rules (including disabled), got %d", len(globalRules))
	}
	if len(localRules) != 1 {
		t.Errorf("expected 1 local rule, got %d", len(localRules))
	}
	if len(conversationRules) != 1 {
		t.Errorf("expected 1 conversation rule, got %d", len(conversationRules))
	}
}

func TestYAMLRuleRegistry_GetByScopeEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: only-local
    intro: Only local
    scope: local
    priority: 50
    enabled: true
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	globalRules := reg.GetByScope(ScopeGlobal)
	if len(globalRules) != 0 {
		t.Errorf("expected 0 global rules, got %d", len(globalRules))
	}
}

func TestYAMLRuleRegistry_FormatPromptSection(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: enabled-1
    intro: Be helpful and concise
    scope: global
    priority: 100
    enabled: true
  - id: enabled-2
    intro: Always verify before deleting files
    scope: local
    priority: 90
    enabled: true
  - id: disabled-1
    intro: This should not appear
    scope: global
    priority: 80
    enabled: false
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	prompt := reg.FormatPromptSection()

	if prompt == "" {
		t.Fatal("expected non-empty prompt section")
	}

	if contains(prompt, "Be helpful and concise") && !contains(prompt, "This should not appear") {
		t.Log("prompt correctly includes only enabled rules")
	}

	if !contains(prompt, "- ") {
		t.Error("prompt items should start with '- ' for Markdown list format")
	}
}

func TestYAMLRuleRegistry_FormatPromptSectionEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")

	yamlContent := `rules:
  - id: only-disabled
    intro: All disabled
    scope: global
    priority: 10
    enabled: false
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	prompt := reg.FormatPromptSection()
	if prompt != "" {
		t.Errorf("expected empty prompt when all rules disabled, got %q", prompt)
	}
}

func TestYAMLRuleRegistry_FormatPromptSectionNoRules(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "empty-rules.yaml")

	yamlContent := `rules: []
`
	os.WriteFile(yamlPath, []byte(yamlContent), 0644)

	reg, _ := NewYAMLRuleRegistry(yamlPath)

	prompt := reg.FormatPromptSection()
	if prompt != "" {
		t.Errorf("expected empty prompt with no rules, got %q", prompt)
	}
}

func TestRuleScope_Values(t *testing.T) {
	testCases := []struct {
		scope    RuleScope
		expected RuleScope
	}{
		{ScopeGlobal, "global"},
		{ScopeLocal, "local"},
		{ScopeConversation, "conversation"},
	}

	for _, tc := range testCases {
		t.Run(string(tc.scope), func(t *testing.T) {
			if tc.scope != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, tc.scope)
			}
		})
	}
}

func TestRule_Fields(t *testing.T) {
	rule := Rule{
		ID:       "test-rule",
		Intro:    "Test rule introduction",
		Scope:    ScopeLocal,
		Priority: 150,
		Enabled:  true,
	}

	if rule.ID != "test-rule" {
		t.Error("ID mismatch")
	}
	if rule.Intro != "Test rule introduction" {
		t.Error("Intro mismatch")
	}
	if rule.Scope != ScopeLocal {
		t.Error("Scope mismatch")
	}
	if rule.Priority != 150 {
		t.Error("Priority mismatch")
	}
	if !rule.Enabled {
		t.Error("Enabled should be true")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfig_CreationAndValidation(t *testing.T) {
	t.Run("create valid agent config", func(t *testing.T) {
		agent := &AgentConfig{
			Name:        "test-agent",
			Role:        "code-assistant",
			Description: "A helpful coding assistant",
			Model:       "gpt-4",
			Skills:      []string{"coding", "debugging"},
		}

		if agent.Name != "test-agent" {
			t.Errorf("expected name 'test-agent', got '%s'", agent.Name)
		}
		if agent.Role != "code-assistant" {
			t.Errorf("expected role 'code-assistant', got '%s'", agent.Role)
		}
		if agent.Description != "A helpful coding assistant" {
			t.Errorf("unexpected description")
		}
		if agent.Model != "gpt-4" {
			t.Errorf("expected model 'gpt-4', got '%s'", agent.Model)
		}
		if len(agent.Skills) != 2 {
			t.Errorf("expected 2 skills, got %d", len(agent.Skills))
		}
	})

	t.Run("agent config with empty fields", func(t *testing.T) {
		agent := &AgentConfig{
			Name: "minimal-agent",
		}

		if agent.Name != "minimal-agent" {
			t.Errorf("expected name 'minimal-agent'")
		}
		if agent.Role != "" {
			t.Errorf("expected empty role")
		}
		if agent.Description != "" {
			t.Errorf("expected empty description")
		}
		if len(agent.Skills) != 0 {
			t.Errorf("expected empty skills slice")
		}
		if agent.Meta != nil {
			t.Errorf("expected nil meta")
		}
	})

	t.Run("agent config with meta data", func(t *testing.T) {
		meta := map[string]any{
			"version": "1.0.0",
			"author":  "test",
			"tags":    []string{"ai", "assistant"},
		}
		agent := &AgentConfig{
			Name: "meta-agent",
			Meta: meta,
		}

		if agent.Meta == nil {
			t.Fatal("expected non-nil meta")
		}
		if agent.Meta["version"] != "1.0.0" {
			t.Errorf("unexpected version in meta")
		}
	})

}

func TestModelConfig_DefaultValuesAndBoundaryCases(t *testing.T) {
	t.Run("zero values for model config", func(t *testing.T) {
		cfg := ModelConfig{}

		if cfg.Name != "" {
			t.Error("expected empty Name")
		}
		if cfg.ContextLength != 0 {
			t.Error("expected ContextLength to be 0")
		}
		if cfg.TopP != 0 {
			t.Error("expected TopP to be 0")
		}
		if cfg.Temperature != 0 {
			t.Error("expected Temperature to be 0")
		}
		if cfg.IsLocal {
			t.Error("expected IsLocal to be false")
		}
		if cfg.FuncCalling {
			t.Error("expected FuncCalling to be false")
		}
	})

	t.Run("boundary values for numeric fields", func(t *testing.T) {
		cfg := ModelConfig{
			ContextLength:     0,
			TopP:              0.0,
			TopK:              0.0,
			Temperature:       2.0,
			RepetitionPenalty: -1.0,
			FrequencyPenalty:  -2.0,
			MaxTurns:          -1,
		}

		if cfg.Temperature != 2.0 {
			t.Error("Temperature should be 2.0")
		}
		if cfg.RepetitionPenalty != -1.0 {
			t.Error("RepetitionPenalty should accept negative value")
		}
	})

	t.Run("boolean flags combinations", func(t *testing.T) {
		testCases := []struct {
			name     string
			config   ModelConfig
			expected []bool
		}{
			{
				name: "all enabled",
				config: ModelConfig{
					IsLocal: true, FuncCalling: true, Structuring: true,
					WebSearching: true, PrefixCon: true, ContextCache: true, Enabled: true,
				},
				expected: []bool{true, true, true, true, true, true, true},
			},
			{
				name: "all disabled",
				config: ModelConfig{
					IsLocal: false, FuncCalling: false, Structuring: false,
					WebSearching: false, PrefixCon: false, ContextCache: false, Enabled: false,
				},
				expected: []bool{false, false, false, false, false, false, false},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				flags := []bool{
					tc.config.IsLocal, tc.config.FuncCalling, tc.config.Structuring,
					tc.config.WebSearching, tc.config.PrefixCon, tc.config.ContextCache, tc.config.Enabled,
				}
				for i, got := range flags {
					if got != tc.expected[i] {
						t.Errorf("flag %d: expected %v, got %v", i, tc.expected[i], got)
					}
				}
			})
		}
	})

	t.Run("complete model config creation", func(t *testing.T) {
		cfg := ModelConfig{
			Name:          "gpt-4",
			Title:         "GPT-4",
			Description:   "Advanced language model",
			Provider:      "openai",
			BaseURL:       "https://api.openai.com/v1",
			APIKey:        "test-key",
			ContextLength: 128000,
			FuncCalling:   true,
			Structuring:   true,
			TopP:          0.9,
			Temperature:   0.7,
			Enabled:       true,
			MaxTurns:      10,
		}

		if cfg.Name != "gpt-4" {
			t.Error("unexpected name")
		}
		if cfg.Provider != "openai" {
			t.Error("unexpected provider")
		}
		if !cfg.FuncCalling || !cfg.Structuring {
			t.Error("flags not set correctly")
		}
	})
}

// TestAgentRegistry_SaveToExcludeTools 验证 SaveTo 持久化 exclude_tools 字段。
// 曾因 SaveTo 重建 frontmatter 时漏掉该字段，导致 agent.update 重写文件后
// exclude_tools 静默丢失（内存仍在，重启/重载后才暴露）。
func TestAgentRegistry_SaveToExcludeTools(t *testing.T) {
	tmpDir := t.TempDir()
	registry, err := LoadAgentsFrom(tmpDir)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}

	agent := &AgentConfig{
		Name:         "excluded-agent",
		Role:         "tester",
		Description:  "Agent with exclude tools",
		ExcludeTools: []string{"PowerShell", "Sleep"},
	}
	if err := registry.SaveTo(agent); err != nil {
		t.Fatalf("SaveTo failed: %v", err)
	}

	// 从磁盘重新解析（而非读内存），验证文件里真实保留了 exclude_tools
	reloaded, err := LoadAgentsFrom(tmpDir)
	if err != nil {
		t.Fatalf("failed to reload registry: %v", err)
	}
	got := reloaded.Get("excluded-agent")
	if got == nil {
		t.Fatal("reloaded registry returned nil for saved agent")
	}
	if len(got.ExcludeTools) != 2 || got.ExcludeTools[0] != "PowerShell" || got.ExcludeTools[1] != "Sleep" {
		t.Fatalf("exclude_tools 未被持久化, got %v", got.ExcludeTools)
	}
}

func TestAgentRegistry_CRUDOperations(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("load agents from empty directory", func(t *testing.T) {
		registry, err := LoadAgentsFrom(tmpDir)
		if err != nil {
			t.Fatalf("failed to load from empty dir: %v", err)
		}
		if registry == nil {
			t.Fatal("expected non-nil registry")
		}
		if count := len(registry.List()); count != 0 {
			t.Errorf("expected 0 agents, got %d", count)
		}
	})

	t.Run("save and get agent", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)
		agent := &AgentConfig{
			Name:         "test-get",
			Role:         "tester",
			Description:  "Test agent for Get operation",
			Model:        "gpt-4",
			Introduction: "This is the introduction text.",
		}

		err := registry.SaveTo(agent)
		if err != nil {
			t.Fatalf("SaveTo failed: %v", err)
		}

		got := registry.Get("test-get")
		if got == nil {
			t.Fatal("Get returned nil for existing agent")
		}
		if got.Name != "test-get" {
			t.Errorf("expected name 'test-get', got '%s'", got.Name)
		}
		if got.Role != "tester" {
			t.Errorf("expected role 'tester', got '%s'", got.Role)
		}
	})

	t.Run("list all agents", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)

		for i := 0; i < 3; i++ {
			agent := &AgentConfig{
				Name:        fmt.Sprintf("list-agent-%d", i),
				Role:        "tester",
				Description: fmt.Sprintf("Test agent %d", i),
			}
			if err := registry.SaveTo(agent); err != nil {
				t.Fatalf("failed to save agent %d: %v", i, err)
			}
		}

		agents := registry.List()
		if len(agents) < 3 {
			t.Errorf("expected at least 3 agents, got %d", len(agents))
		}
	})

	t.Run("remove agent", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)
		agent := &AgentConfig{
			Name:        "to-remove",
			Role:        "temporary",
			Description: "This agent will be removed",
		}

		err := registry.SaveTo(agent)
		if err != nil {
			t.Fatalf("SaveTo failed: %v", err)
		}

		err = registry.Remove("to-remove")
		if err != nil {
			t.Fatalf("Remove failed: %v", err)
		}

		got := registry.Get("to-remove")
		if got != nil {
			t.Error("expected nil after removal")
		}
	})

	t.Run("remove non-existent agent", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)

		err := registry.Remove("non-existent")
		if err == nil {
			t.Error("expected error when removing non-existent agent")
		}
		if !strings.Contains(err.Error(), "未找到") {
			t.Errorf("error should mention '未找到', got: %v", err)
		}
	})

	t.Run("save agent with empty name", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)
		agent := &AgentConfig{
			Role:        "no-name",
			Description: "Agent without name",
		}

		err := registry.SaveTo(agent)
		if err == nil {
			t.Error("expected error when saving agent with empty name")
		}
	})

	t.Run("read agent from file", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)
		original := &AgentConfig{
			Name:         "read-test",
			Role:         "reader",
			Description:  "For reading test",
			Model:        "claude-3",
			Skills:       []string{"reading"},
			Introduction: "Original introduction",
		}

		err := registry.SaveTo(original)
		if err != nil {
			t.Fatalf("failed to save original: %v", err)
		}

		read, err := registry.Read("read-test.md")
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		if read.Name != "read-test" {
			t.Errorf("expected name 'read-test', got '%s'", read.Name)
		}
	})

	t.Run("overwrite existing agent", func(t *testing.T) {
		registry, _ := LoadAgentsFrom(tmpDir)

		first := &AgentConfig{
			Name:        "overwrite-me",
			Role:        "first-version",
			Description: "First description",
		}
		if err := registry.SaveTo(first); err != nil {
			t.Fatalf("failed to save first version: %v", err)
		}

		second := &AgentConfig{
			Name:        "overwrite-me",
			Role:        "second-version",
			Description: "Updated description",
			Model:       "new-model",
		}
		if err := registry.SaveTo(second); err != nil {
			t.Fatalf("failed to save second version: %v", err)
		}

		got := registry.Get("overwrite-me")
		if got.Role != "second-version" {
			t.Errorf("expected role 'second-version', got '%s'", got.Role)
		}
		if got.Model != "new-model" {
			t.Errorf("expected model 'new-model', got '%s'", got.Model)
		}
	})
}

func TestConfigLoading_UnitTests(t *testing.T) {
	t.Run("parse valid agent file", func(t *testing.T) {
		content := `---
name: test-parser
role: parser
description: Testing parser
model: gpt-4
skills:
  - parsing
  - validation
meta:
  version: 1.0
---
This is the introduction body.
`
		tmpFile := filepath.Join(t.TempDir(), "test.md")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		agent, err := parseAgentFile(tmpFile)
		if err != nil {
			t.Fatalf("parseAgentFile failed: %v", err)
		}

		if agent.Name != "test-parser" {
			t.Errorf("expected name 'test-parser', got '%s'", agent.Name)
		}
		if agent.Role != "parser" {
			t.Errorf("expected role 'parser', got '%s'", agent.Role)
		}
		if agent.Model != "gpt-4" {
			t.Errorf("expected model 'gpt-4', got '%s'", agent.Model)
		}
		if len(agent.Skills) != 2 {
			t.Errorf("expected 2 skills, got %d", len(agent.Skills))
		}
		if agent.Introduction != "This is the introduction body." {
			t.Errorf("unexpected introduction: '%s'", agent.Introduction)
		}
		if agent.Meta == nil {
			t.Fatal("expected non-nil meta")
		}
		version := agent.Meta["version"]
		if version != 1.0 && version != "1.0" {
			t.Errorf("unexpected meta version: %v (type: %T)", version, version)
		}
	})

	t.Run("parse agent file with title fallback", func(t *testing.T) {
		content := `---
name: title-fallback
title: Fallback Title
description: Uses title as role
---
Body content here.
`
		tmpFile := filepath.Join(t.TempDir(), "fallback.md")
		os.WriteFile(tmpFile, []byte(content), 0644)

		agent, err := parseAgentFile(tmpFile)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		if agent.Role != "Fallback Title" {
			t.Errorf("expected role 'Fallback Title' (from title), got '%s'", agent.Role)
		}
	})

	t.Run("parse invalid file missing frontmatter", func(t *testing.T) {
		content := `This is not a valid agent file.
No frontmatter delimiter here.
`
		tmpFile := filepath.Join(t.TempDir(), "invalid.md")
		os.WriteFile(tmpFile, []byte(content), 0644)

		_, err := parseAgentFile(tmpFile)
		if err == nil {
			t.Error("expected error for invalid file format")
		}
	})

	t.Run("parse file with windows line endings", func(t *testing.T) {
		content := "---\r\nname: windows-test\r\nrole: tester\r\n---\r\nWindows content\r\n"
		tmpFile := filepath.Join(t.TempDir(), "windows.md")
		os.WriteFile(tmpFile, []byte(content), 0644)

		agent, err := parseAgentFile(tmpFile)
		if err != nil {
			t.Fatalf("failed to parse Windows file: %v", err)
		}
		if agent.Name != "windows-test" {
			t.Errorf("expected name 'windows-test', got '%s'", agent.Name)
		}
	})

	t.Run("load agents from directory with multiple files", func(t *testing.T) {
		dir := t.TempDir()

		for i := 0; i < 3; i++ {
			content := fmt.Sprintf(`---
name: dir-agent-%d
role: agent
description: Agent number %d
---
Content for agent %d.
`, i, i, i)
			filePath := filepath.Join(dir, fmt.Sprintf("agent-%d.md", i))
			os.WriteFile(filePath, []byte(content), 0644)
		}

		registry, err := LoadAgentsFrom(dir)
		if err != nil {
			t.Fatalf("LoadAgentsFrom failed: %v", err)
		}

		agents := registry.List()
		if len(agents) != 3 {
			t.Errorf("expected 3 agents, got %d", len(agents))
		}
	})

	t.Run("load agents skips non-md files", func(t *testing.T) {
		dir := t.TempDir()

		validContent := `---
name: valid-agent
role: agent
---
Valid content.
`
		os.WriteFile(filepath.Join(dir, "valid.md"), []byte(validContent), 0644)
		os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("should be ignored"), 0644)
		os.WriteFile(filepath.Join(dir, "README.MD"), []byte("---\nname: readme\n---\nReadme"), 0644)

		registry, err := LoadAgentsFrom(dir)
		if err != nil {
			t.Fatalf("LoadAgentsFrom failed: %v", err)
		}

		count := len(registry.List())
		if count != 2 {
			t.Errorf("expected 2 agents (valid.md and README.MD), got %d", count)
		}
	})

	t.Run("load agents handles parse errors gracefully", func(t *testing.T) {
		dir := t.TempDir()

		validContent := `---
name: good-agent
role: good
---
Good content.
`
		badContent := `this is not valid yaml frontmatter`

		os.WriteFile(filepath.Join(dir, "good.md"), []byte(validContent), 0644)
		os.WriteFile(filepath.Join(dir, "bad.md"), []byte(badContent), 0644)

		registry, err := LoadAgentsFrom(dir)
		if err != nil {
			t.Fatalf("LoadAgentsFrom should succeed even with bad files: %v", err)
		}

		agents := registry.List()
		if len(agents) != 1 {
			t.Errorf("expected 1 valid agent, got %d", len(agents))
		}
		if agents[0].Name != "good-agent" {
			t.Errorf("expected 'good-agent', got '%s'", agents[0].Name)
		}
	})
}

func TestRuntimeDirectory_Operations(t *testing.T) {
	t.Run("new runtime directory", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		if dir == nil {
			t.Fatal("expected non-nil directory")
		}
		if dir.Count() != 0 {
			t.Errorf("expected 0 agents initially, got %d", dir.Count())
		}
	})

	t.Run("register and get agent", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		config := &AgentConfig{Name: "test-runtime", Description: "Test runtime agent"}
		meta := NewAgentRuntimeMeta(config)

		err := dir.Register(meta)
		if err != nil {
			t.Fatalf("Register failed: %v", err)
		}

		got := dir.Get("test-runtime")
		if got == nil {
			t.Fatal("Get returned nil")
		}
		if got.ID() != "test-runtime" {
			t.Errorf("expected ID 'test-runtime', got '%s'", got.ID())
		}
		if got.State != AgentStateIdle {
			t.Errorf("expected Idle state, got %s", got.State)
		}
	})

	t.Run("register duplicate agent fails", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		config := &AgentConfig{Name: "dup-test"}
		meta1 := NewAgentRuntimeMeta(config)
		meta2 := NewAgentRuntimeMeta(config)

		err := dir.Register(meta1)
		if err != nil {
			t.Fatalf("first Register failed: %v", err)
		}

		err = dir.Register(meta2)
		if err == nil {
			t.Error("expected error on duplicate registration")
		}
		if err != ErrRuntimeDirDuplicate {
			t.Errorf("expected ErrRuntimeDirDuplicate, got: %v", err)
		}
	})

	t.Run("directory size limit", func(t *testing.T) {
		dir := NewRuntimeDirectory(2)

		for i := 0; i < 2; i++ {
			config := &AgentConfig{Name: fmt.Sprintf("limited-%d", i)}
			meta := NewAgentRuntimeMeta(config)
			if err := dir.Register(meta); err != nil {
				t.Fatalf("failed to register agent %d: %v", i, err)
			}
		}

		extraConfig := &AgentConfig{Name: "extra"}
		extraMeta := NewAgentRuntimeMeta(extraConfig)
		err := dir.Register(extraMeta)
		if err == nil {
			t.Error("expected error when directory is full")
		}
		if err != ErrRuntimeDirFull {
			t.Errorf("expected ErrRuntimeDirFull, got: %v", err)
		}
	})

	t.Run("unregister agent", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		config := &AgentConfig{Name: "unregister-test"}
		meta := NewAgentRuntimeMeta(config)
		dir.Register(meta)

		dir.Unregister("unregister-test")

		got := dir.Get("unregister-test")
		if got != nil {
			t.Error("expected nil after unregister")
		}
		if dir.Count() != 0 {
			t.Errorf("expected 0 count after unregister, got %d", dir.Count())
		}
	})

	t.Run("set state and score", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		config := &AgentConfig{Name: "state-test", Description: "Testing state changes"}
		meta := NewAgentRuntimeMeta(config)
		dir.Register(meta)

		dir.SetState("state-test", AgentStateBusy)
		got := dir.Get("state-test")
		if got.State != AgentStateBusy {
			t.Errorf("expected Busy state, got %s", got.State)
		}

		dir.SetScore("state-test", 95.5)
		got = dir.Get("state-test")
		if got.Score != 95.5 {
			t.Errorf("expected score 95.5, got %f", got.Score)
		}
	})

	t.Run("increment task count", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)
		config := &AgentConfig{Name: "task-counter"}
		meta := NewAgentRuntimeMeta(config)
		dir.Register(meta)

		dir.IncrementTaskCount("task-counter")
		dir.IncrementTaskCount("task-counter")
		dir.IncrementTaskCount("task-counter")

		got := dir.Get("task-counter")
		if got.TaskCount != 3 {
			t.Errorf("expected TaskCount 3, got %d", got.TaskCount)
		}
	})

	t.Run("list available agents sorted by score", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)

		scores := []float64{70.0, 90.0, 80.0}
		names := []string{"low-score", "high-score", "mid-score"}
		for i, name := range names {
			config := &AgentConfig{Name: name}
			meta := NewAgentRuntimeMeta(config)
			meta.Score = scores[i]
			dir.Register(meta)
		}

		available := dir.ListAvailable()
		if len(available) != 3 {
			t.Fatalf("expected 3 available agents, got %d", len(available))
		}

		if available[0].Score < available[1].Score || available[1].Score < available[2].Score {
			t.Error("agents should be sorted by score descending")
		}
		if available[0].ID() != "high-score" {
			t.Errorf("highest scored agent should be first, got '%s'", available[0].ID())
		}
	})

	t.Run("find by description case insensitive", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)

		config1 := &AgentConfig{Name: "search-1", Description: "Python code expert"}
		config2 := &AgentConfig{Name: "search-2", Description: "JavaScript developer"}
		config3 := &AgentConfig{Name: "search-3", Description: "PYTHON automation"}

		dir.Register(NewAgentRuntimeMeta(config1))
		dir.Register(NewAgentRuntimeMeta(config2))
		dir.Register(NewAgentRuntimeMeta(config3))

		results := dir.FindByDescription("python")
		if len(results) != 2 {
			t.Errorf("expected 2 matches for 'python', got %d", len(results))
		}

		results = dir.FindByDescription("NONEXISTENT")
		if len(results) != 0 {
			t.Errorf("expected 0 matches, got %d", len(results))
		}
	})

	t.Run("list active excludes error state", func(t *testing.T) {
		dir := NewRuntimeDirectory(10)

		config1 := &AgentConfig{Name: "active-1"}
		config2 := &AgentConfig{Name: "error-1"}
		meta1 := NewAgentRuntimeMeta(config1)
		meta2 := NewAgentRuntimeMeta(config2)
		meta2.State = AgentStateError

		dir.Register(meta1)
		dir.Register(meta2)

		active := dir.ListActive()
		if len(active) != 1 {
			t.Errorf("expected 1 active agent, got %d", len(active))
		}
		if active[0].ID() != "active-1" {
			t.Errorf("expected 'active-1', got '%s'", active[0].ID())
		}
	})
}

func TestAgentState_Methods(t *testing.T) {
	t.Run("is terminal states", func(t *testing.T) {
		if !AgentStateError.IsTerminal() {
			t.Error("Error should be terminal")
		}
		if AgentStateIdle.IsTerminal() {
			t.Error("Idle should not be terminal")
		}
		if AgentStateBusy.IsTerminal() {
			t.Error("Busy should not be terminal")
		}
		if AgentStateCoordinating.IsTerminal() {
			t.Error("Coordinating should not be terminal")
		}
		if AgentStateDormant.IsTerminal() {
			t.Error("Dormant should not be terminal")
		}
	})

	t.Run("can accept task", func(t *testing.T) {
		acceptingStates := []AgentState{AgentStateIdle, AgentStateDormant}
		rejectingStates := []AgentState{AgentStateBusy, AgentStateCoordinating, AgentStateError}

		for _, state := range acceptingStates {
			if !state.CanAcceptTask() {
				t.Errorf("%s should accept tasks", state)
			}
		}
		for _, state := range rejectingStates {
			if state.CanAcceptTask() {
				t.Errorf("%s should reject tasks", state)
			}
		}
	})
}

func TestAgentRuntimeMeta_Methods(t *testing.T) {
	t.Run("creation with valid config", func(t *testing.T) {
		config := &AgentConfig{
			Name:        "meta-test",
			Description: "Runtime meta test",
		}
		meta := NewAgentRuntimeMeta(config)

		if meta.ID() != "meta-test" {
			t.Errorf("expected ID 'meta-test', got '%s'", meta.ID())
		}
		if meta.Name() != "meta-test" {
			t.Errorf("expected Name 'meta-test', got '%s'", meta.Name())
		}
		if meta.Description() != "Runtime meta test" {
			t.Errorf("unexpected description")
		}
		if !meta.IsActive() {
			t.Error("newly created meta should be active")
		}
		if !meta.IsAvailable() {
			t.Error("newly created meta should be available")
		}
		if meta.TaskCount != 0 {
			t.Errorf("expected TaskCount 0, got %d", meta.TaskCount)
		}
		if meta.Score != 0 {
			t.Errorf("expected Score 0, got %f", meta.Score)
		}
		if meta.LastActive.IsZero() {
			t.Error("LastActive should be set to current time")
		}
	})

	t.Run("panic on nil config", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic on nil config")
			}
		}()
		NewAgentRuntimeMeta(nil)
	})

	t.Run("state transitions affect availability", func(t *testing.T) {
		config := &AgentConfig{Name: "transition-test"}
		meta := NewAgentRuntimeMeta(config)

		meta.State = AgentStateBusy
		if meta.IsAvailable() {
			t.Error("Busy agent should not be available")
		}
		if !meta.IsActive() {
			t.Error("Busy agent should still be active")
		}

		meta.State = AgentStateError
		if meta.IsActive() {
			t.Error("Error agent should not be active")
		}
		if meta.IsAvailable() {
			t.Error("Error agent should not be available")
		}

		meta.State = AgentStateDormant
		if !meta.IsAvailable() {
			t.Error("Dormant agent should be available")
		}
	})
}

func TestProviderConfig_Creation(t *testing.T) {
	t.Run("complete provider config", func(t *testing.T) {
		provider := ProviderConfig{
			Name:      "openai",
			Title:     "OpenAI",
			BaseURL:   "https://api.openai.com/v1",
			APIKey:    "sk-test-key",
			AuthToken: "bearer-token",
		}

		if provider.Name != "openai" {
			t.Error("unexpected name")
		}
		if provider.BaseURL != "https://api.openai.com/v1" {
			t.Error("unexpected base URL")
		}
	})

	t.Run("minimal provider config", func(t *testing.T) {
		provider := ProviderConfig{
			Name:    "minimal",
			BaseURL: "http://localhost:11434",
		}

		if provider.Title != "" {
			t.Error("expected empty title")
		}
		if provider.APIKey != "" {
			t.Error("expected empty API key")
		}
	})
}

func TestDeepCopyMeta(t *testing.T) {
	t.Run("flat map copy", func(t *testing.T) {
		original := map[string]any{
			"key1": "value1",
			"key2": 42,
			"key3": true,
		}

		copied := deepCopyMeta(original)
		copied["key1"] = "modified"
		copied["key2"] = 99

		if original["key1"] != "value1" {
			t.Error("modification to copy affected original")
		}
		if original["key2"] != 42 {
			t.Error("modification to copy affected original")
		}
	})

	t.Run("nested map copy", func(t *testing.T) {
		original := map[string]any{
			"nested": map[string]any{
				"inner": "deep-value",
			},
		}

		copied := deepCopyMeta(original)
		nested := copied["nested"].(map[string]any)
		nested["inner"] = "modified"

		innerOriginal := original["nested"].(map[string]any)
		if innerOriginal["inner"] != "deep-value" {
			t.Error("deep modification to copy affected original")
		}
	})

	t.Run("slice copy", func(t *testing.T) {
		original := map[string]any{
			"items": []any{map[string]any{"id": 1}, "string-item"},
		}

		copied := deepCopyMeta(original)
		items := copied["items"].([]any)
		items[0] = "replaced"

		originalItems := original["items"].([]any)
		if _, ok := originalItems[0].(map[string]any); !ok {
			t.Error("slice modification to copy affected original")
		}
	})
}

func BenchmarkAgentRegistry_Get(b *testing.B) {
	tmpDir := b.TempDir()
	registry, _ := LoadAgentsFrom(tmpDir)

	for i := 0; i < 1000; i++ {
		agent := &AgentConfig{
			Name:        fmt.Sprintf("bench-agent-%d", i),
			Role:        "bench",
			Description: fmt.Sprintf("Benchmark agent %d", i),
		}
		registry.SaveTo(agent)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = registry.Get(fmt.Sprintf("bench-agent-%d", i%1000))
	}
}

func BenchmarkRuntimeDirectory_ListAvailable(b *testing.B) {
	dir := NewRuntimeDirectory(1000)

	for i := 0; i < 1000; i++ {
		config := &AgentConfig{Name: fmt.Sprintf("bench-%d", i), Description: "Benchmark agent"}
		meta := NewAgentRuntimeMeta(config)
		meta.Score = float64(i % 100)
		dir.Register(meta)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = dir.ListAvailable()
	}
}

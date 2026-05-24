package tools

import (
	"testing"

	"github.com/DotNetAge/goreact/core"
)

func TestFallbackPermissionChecker_AllowSafeTool(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &core.ToolUseContext{
		ToolName: "read_file",
		ToolInfo: &core.ToolInfo{
			Name:          "read_file",
			IsReadOnly:    true,
			SecurityLevel: core.LevelSafe,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != core.PermissionAllow {
		t.Errorf("safe tool: expected Allow, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AllowSafeLevel(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &core.ToolUseContext{
		ToolName: "list_files",
		ToolInfo: &core.ToolInfo{
			Name:          "list_files",
			IsReadOnly:    false,
			SecurityLevel: core.LevelSafe,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != core.PermissionAllow {
		t.Errorf("level-safe tool: expected Allow, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AskSensitive(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &core.ToolUseContext{
		ToolName: "edit_file",
		ToolInfo: &core.ToolInfo{
			Name:          "edit_file",
			IsReadOnly:    false,
			SecurityLevel: core.LevelSensitive,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != core.PermissionAsk {
		t.Errorf("sensitive tool: expected Ask, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AskHighRisk(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &core.ToolUseContext{
		ToolName: "bash",
		ToolInfo: &core.ToolInfo{
			Name:          "bash",
			IsReadOnly:    false,
			SecurityLevel: core.LevelHighRisk,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != core.PermissionAsk {
		t.Errorf("high-risk tool: expected Ask, got %s", result.Behavior)
	}
}

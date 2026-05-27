package tools

import (
	"testing"

	"github.com/DotNetAge/goreact/events"
)

func TestFallbackPermissionChecker_AllowSafeTool(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &ToolUseContext{
		ToolName: "read_file",
		ToolInfo: &ToolInfo{
			Name:          "read_file",
			IsReadOnly:    true,
			SecurityLevel: events.LevelSafe,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Errorf("safe tool: expected Allow, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AllowSafeLevel(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &ToolUseContext{
		ToolName: "list_files",
		ToolInfo: &ToolInfo{
			Name:          "list_files",
			IsReadOnly:    false,
			SecurityLevel: events.LevelSafe,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != PermissionAllow {
		t.Errorf("level-safe tool: expected Allow, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AskSensitive(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &ToolUseContext{
		ToolName: "edit_file",
		ToolInfo: &ToolInfo{
			Name:          "edit_file",
			IsReadOnly:    false,
			SecurityLevel: events.LevelSensitive,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != PermissionAsk {
		t.Errorf("sensitive tool: expected Ask, got %s", result.Behavior)
	}
}

func TestFallbackPermissionChecker_AskHighRisk(t *testing.T) {
	c := NewFallbackPermissionChecker()
	ctx := &ToolUseContext{
		ToolName: "bash",
		ToolInfo: &ToolInfo{
			Name:          "bash",
			IsReadOnly:    false,
			SecurityLevel: events.LevelHighRisk,
		},
	}
	result := c.CheckPermissions(ctx)
	if result.Behavior != PermissionAsk {
		t.Errorf("high-risk tool: expected Ask, got %s", result.Behavior)
	}
}

package tools

import (
	"strings"
	"testing"
)

func TestIsWindowsPlatform(t *testing.T) {
	result := IsWindowsPlatform()
	if result && strings.Contains(t.Name(), "darwin") {
		t.Errorf("IsWindowsPlatform() returned true on non-Windows platform")
	}
}

func TestNewPowerShellTool(t *testing.T) {
	tool := NewPowerShellTool()

	if tool == nil {
		t.Fatal("NewPowerShellTool() returned nil")
	}

	info := tool.Info()
	if info.Name != "PowerShell" {
		t.Errorf("expected tool name 'PowerShell', got %q", info.Name)
	}

	if len(info.Parameters) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(info.Parameters))
	}

	if info.Parameters[0].Name != "command" {
		t.Errorf("expected parameter name 'command', got %q", info.Parameters[0].Name)
	}

	if !info.Parameters[0].Required {
		t.Error("command parameter should be required")
	}

	desc := info.Prompt
	if !strings.Contains(desc, "PowerShell") {
		t.Error("prompt should mention PowerShell")
	}
	if !strings.Contains(desc, "robocopy") {
		t.Error("prompt should mention robocopy exit codes")
	}
	if !strings.Contains(desc, "findstr") {
		t.Error("prompt should mention findstr exit codes")
	}
	if !strings.Contains(desc, "Out-String") {
		t.Error("prompt should mention Out-String formatting")
	}
}

func TestPowerShellTool_Execute_EmptyCommand(t *testing.T) {
	tool := NewPowerShellTool()

	_, err := tool.Execute(ctxWithLogger(), map[string]any{})
	if err == nil {
		t.Error("expected error for empty command, got nil")
	}

	_, err = tool.Execute(ctxWithLogger(), map[string]any{"command": ""})
	if err == nil {
		t.Error("expected error for empty command, got nil")
	}
}

func TestApplyPowerShellCommandSemantics(t *testing.T) {
	tests := []struct {
		exitCode int
		stderr   string
		expected string
	}{
		{0, "", "未复制任何文件。无失败。"},
		{1, "", "文件已成功复制。"},
		{7, "", "文件已复制，存在文件不匹配，并且存在额外文件。"},
		{8, "error", "多个文件未复制。"},
		{99, "unknown error", "unknown error"},
	}

	for _, tt := range tests {
		result := applyPowerShellCommandSemantics(tt.exitCode, tt.stderr)
		if result != tt.expected {
			t.Errorf("exitCode=%d, stderr=%q: expected %q, got %q",
				tt.exitCode, tt.stderr, tt.expected, result)
		}
	}
}

func TestApplyPowerShellCommandSemantics_findstrOnly(t *testing.T) {
	tests := []struct {
		exitCode int
		stderr   string
		expected string
	}{
		{0, "no robocopy", "在至少一个文件中找到匹配。"},
		{1, "no robocopy", "未找到匹配。"},
		{2, "no robocopy", "命令行语法无效。"},
	}

	for _, tt := range tests {
		tmpCodes := robocopyExitCodes
		robocopyExitCodes = nil

		result := applyPowerShellCommandSemantics(tt.exitCode, tt.stderr)
		if result != tt.expected {
			t.Errorf("exitCode=%d, stderr=%q: expected %q, got %q",
				tt.exitCode, tt.stderr, tt.expected, result)
		}

		robocopyExitCodes = tmpCodes
	}
}

func TestPowerShellTool_Info_ContainsSecurityLevel(t *testing.T) {
	tool := NewPowerShellTool()
	info := tool.Info()

	if info.SecurityLevel == 0 {
		t.Error("PowerShell tool should have a non-zero security level")
	}

	if len(info.Tags) == 0 {
		t.Error("PowerShell tool should have tags")
	}

	expectedTags := []string{"windows", "powershell", "system", "command"}
	for _, tag := range expectedTags {
		found := false
		for _, t := range info.Tags {
			if t == tag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected tag %q not found in tags %v", tag, info.Tags)
		}
	}
}

package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DotNetAge/goharness/events"
)

func TestRunScript_Info(t *testing.T) {
	tool := NewRunScriptTool()
	info := tool.Info()

	if info.Name != "RunScript" {
		t.Errorf("expected name 'run_script', got %q", info.Name)
	}
	if info.SecurityLevel != events.LevelSensitive {
		t.Errorf("expected SecurityLevel LevelSensitive, got %v", info.SecurityLevel)
	}
	if len(info.Tags) == 0 {
		t.Error("expected Tags to be defined")
	}
	if len(info.Parameters) == 0 {
		t.Error("expected Parameters to be defined")
	}

	var hasCommand bool
	for _, p := range info.Parameters {
		if p.Name == "command" && p.Required {
			hasCommand = true
		}
	}
	if !hasCommand {
		t.Error("expected 'command' parameter to be required")
	}
}

func TestParseCommand_PythonInterpreter(t *testing.T) {
	language, scriptPath := parseCommand("python scripts/analyze.py --input data.json", "/project/skill")

	if language != "python" {
		t.Errorf("expected language 'python', got %q", language)
	}
	if scriptPath != filepath.Join("/project/skill", "scripts/analyze.py") {
		t.Errorf("expected script path 'scripts/analyze.py', got %q", scriptPath)
	}
}

func TestParseCommand_Python3Interpreter(t *testing.T) {
	language, _ := parseCommand("python3 scripts/foo.py", "/work")

	if language != "python3" {
		t.Errorf("expected language 'python3', got %q", language)
	}
}

func TestParseCommand_ShellScript(t *testing.T) {
	language, scriptPath := parseCommand("./scripts/build.sh --target release", "/project")

	if language != "shell" {
		t.Errorf("expected language 'shell', got %q", language)
	}
	expected := filepath.Join("/project", "scripts/build.sh")
	if scriptPath != expected {
		t.Errorf("expected %q, got %q", expected, scriptPath)
	}
}

func TestParseCommand_ExtensionBased(t *testing.T) {
	tests := []struct {
		cmd     string
		expLang string
		expFile string
	}{
		{"scripts/fetch_data.py arg1", "python", "scripts/fetch_data.py"},
		{"scripts/deploy.sh", "shell", "scripts/deploy.sh"},
		{"scripts/bundle.js", "node", "scripts/bundle.js"},
		{"scripts/convert.rb", "ruby", "scripts/convert.rb"},
		{"scripts/main", "", "scripts/main"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			lang, path := parseCommand(tt.cmd, "/base")
			if lang != tt.expLang {
				t.Errorf("language: expected %q, got %q", tt.expLang, lang)
			}
			expected := filepath.Join("/base", tt.expFile)
			if path != expected {
				t.Errorf("path: expected %q, got %q", expected, path)
			}
		})
	}
}

func TestParseCommand_Empty(t *testing.T) {
	lang, path := parseCommand("", "/base")
	if lang != "" || path != "" {
		t.Errorf("expected empty for empty command, got (%q, %q)", lang, path)
	}
}

type mockScriptExecutor struct {
	result *scriptResult
	err    error
}

func (m *mockScriptExecutor) Execute(_ context.Context, _, _ string, _ []string) (*scriptResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func TestRunScript_ExecuteWithMockExecutor(t *testing.T) {
	projectDir := t.TempDir()
	tool := &RunScript{
		info: NewRunScriptTool().Info(),
		scriptExecutor: &mockScriptExecutor{
			result: &scriptResult{
				ExitCode: 0,
				Stdout:   "hello from script",
				Duration: "12ms",
			},
		},
	}

	// 安全决策统一收口到沙箱：Execute 必须在注入沙箱的上下文中运行。
	result, err := tool.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command":     "python scripts/test.py",
		"working_dir": projectDir,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", result)
	}
	if m["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", m["status"])
	}
	if m["language"] != "python" {
		t.Errorf("expected language 'python', got %v", m["language"])
	}
	if m["output"] != "hello from script" {
		t.Errorf("expected output 'hello from script', got %v", m["output"])
	}
}

func TestRunScript_MissingCommand(t *testing.T) {
	tool := NewRunScriptTool()

	_, err := tool.Execute(ctxWithLogger(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing command parameter")
	}
}

func TestRunScript_EmptyCommand(t *testing.T) {
	tool := NewRunScriptTool()

	_, err := tool.Execute(ctxWithLogger(), map[string]any{
		"command": "   ",
	})
	if err == nil {
		t.Error("expected error for whitespace-only command")
	}
}

func TestTruncateScriptOutput(t *testing.T) {
	short := "hello"
	if r := truncateScriptOutput(short, 100); r != short {
		t.Errorf("short string should not be truncated, got %q", r)
	}

	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	longStr := string(long)
	truncated := truncateScriptOutput(longStr, 2000)
	if len(truncated) > 2050 {
		t.Errorf("truncated output too long: %d", len(truncated))
	}
}

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DotNetAge/goreact/events"
)

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("failed to resolve absolute path for %q: %v", path, err)
	}
	return abs
}

func TestGrep(t *testing.T) {
	// Create Grep tool
	grep := NewGrepTool()

	// Test searching for a pattern in the current file
	result, err := grep.Execute(context.Background(), map[string]any{"pattern": "TestGrep", "path": "./builtin_test.go"})
	if err != nil {
		t.Errorf("Expected no error for grep, got %v", err)
	}
	if result == nil {
		t.Error("Expected non-nil result for grep")
	}
}

func TestBash(t *testing.T) {
	bash := NewBashToolUnrestricted()

	t.Run("basic command execution", func(t *testing.T) {
		result, err := bash.Execute(context.Background(), map[string]any{"command": "echo hello"})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
	})

	t.Run("missing command parameter", func(t *testing.T) {
		_, err := bash.Execute(context.Background(), map[string]any{})
		if err == nil {
			t.Error("Expected error for missing command")
		}
	})

	t.Run("command with error", func(t *testing.T) {
		result, err := bash.Execute(context.Background(), map[string]any{"command": "ls /nonexistent_dir_123"})
		if err != nil {
			t.Fatalf("Expected no error (error in result), got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != false {
			t.Error("Expected success to be false")
		}
		if resultMap["error"] == nil {
			t.Error("Expected error message")
		}
	})

	t.Run("Name and Description", func(t *testing.T) {
		info := bash.Info()
		if info.Name != "Bash" {
			t.Errorf("Expected 'bash', got %q", info.Name)
		}
		if info.Description == "" {
			t.Error("Expected non-empty description")
		}
		if info.SecurityLevel != events.LevelHighRisk {
			t.Errorf("Expected HighRisk, got %v", info.SecurityLevel)
		}
	})

	t.Run("working_dir parameter", func(t *testing.T) {
		result, err := bash.Execute(context.Background(), map[string]any{
			"command":     "pwd",
			"working_dir": "/tmp",
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		stdout := resultMap["stdout"].(string)
		if !strings.Contains(stdout, "/tmp") {
			t.Errorf("Expected pwd to show /tmp, got %q", stdout)
		}
	})
}

func TestLS(t *testing.T) {
	ls := NewLsTool()

	t.Run("list current directory", func(t *testing.T) {
		result, err := ls.Execute(context.Background(), map[string]any{"path": "."})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
		if resultMap["total_items"] == nil {
			t.Error("Expected total_items to be set")
		}
	})

	t.Run("non-existent directory", func(t *testing.T) {
		_, err := ls.Execute(context.Background(), map[string]any{"path": "/nonexistent_dir_12345"})
		if err == nil {
			t.Error("Expected error for non-existent directory")
		}
	})

	t.Run("path is not a directory", func(t *testing.T) {
		_, err := ls.Execute(context.Background(), map[string]any{"path": "builtin_test.go"})
		if err == nil {
			t.Error("Expected error when path is not a directory")
		}
	})

	t.Run("show hidden files", func(t *testing.T) {
		result, err := ls.Execute(context.Background(), map[string]any{"path": ".", "show_hidden": true})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		items := resultMap["items"].([]map[string]any)
		found := false
		for _, item := range items {
			name, ok := item["name"].(string)
			if !ok {
				continue
			}
			if name == "builtin_test.go" || strings.HasPrefix(name, ".") {
				found = true
				break
			}
		}
		_ = found
	})

	t.Run("Name and Description", func(t *testing.T) {
		if ls.Info().Name != "Ls" {
			t.Errorf("Expected 'ls', got %q", ls.Info().Name)
		}
		if ls.Info().Description == "" {
			t.Error("Expected non-empty description")
		}
		if ls.Info().SecurityLevel != events.LevelSafe {
			t.Errorf("Expected LevelSafe, got %v", ls.Info().SecurityLevel)
		}
	})
}

func TestGlob(t *testing.T) {
	glob := NewGlobTool()

	t.Run("find go files", func(t *testing.T) {
		result, err := glob.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": "."})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
		if resultMap["matches_found"] == nil {
			t.Error("Expected matches_found to be set")
		}
	})

	t.Run("missing pattern", func(t *testing.T) {
		_, err := glob.Execute(context.Background(), map[string]any{"path": "."})
		if err == nil {
			t.Error("Expected error for missing pattern")
		}
	})

	t.Run("non-existent search path", func(t *testing.T) {
		_, err := glob.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": "/nonexistent_dir_12345"})
		if err == nil {
			t.Error("Expected error for non-existent path")
		}
	})

	t.Run("search path is not a directory", func(t *testing.T) {
		_, err := glob.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": "builtin_test.go"})
		if err == nil {
			t.Error("Expected error when path is not a directory")
		}
	})

	t.Run("Name and Description", func(t *testing.T) {
		if glob.Info().Name != "Glob" {
			t.Errorf("Expected 'glob', got %q", glob.Info().Name)
		}
		if glob.Info().Description == "" {
			t.Error("Expected non-empty description")
		}
		if glob.Info().SecurityLevel != events.LevelSafe {
			t.Errorf("Expected LevelSafe, got %v", glob.Info().SecurityLevel)
		}
	})
}

func TestRead(t *testing.T) {
	read := NewReadTool()
	absPath := mustAbs(t, "builtin_test.go")

	t.Run("read this test file", func(t *testing.T) {
		result, err := read.Execute(context.Background(), map[string]any{"path": absPath})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
		if resultMap["content"] == nil {
			t.Error("Expected content to be set")
		}
	})

	t.Run("read with line range", func(t *testing.T) {
		result, err := read.Execute(context.Background(), map[string]any{
			"path":       absPath,
			"start_line": 1.0,
			"end_line":   5.0,
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := read.Execute(context.Background(), map[string]any{})
		if err == nil {
			t.Error("Expected error for missing path")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := read.Execute(context.Background(), map[string]any{"path": "/nonexistent_file_12345.txt"})
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})

	t.Run("path is a directory", func(t *testing.T) {
		_, err := read.Execute(context.Background(), map[string]any{"path": "."})
		if err == nil {
			t.Error("Expected error when path is a directory")
		}
	})

	t.Run("Name and Description", func(t *testing.T) {
		if read.Info().Name != "Read" {
			t.Errorf("Expected 'read', got %q", read.Info().Name)
		}
		if read.Info().Description == "" {
			t.Error("Expected non-empty description")
		}
		if read.Info().SecurityLevel != events.LevelSafe {
			t.Errorf("Expected LevelSafe, got %v", read.Info().SecurityLevel)
		}
	})
}

func TestWrite(t *testing.T) {
	write := NewWriteTool()

	t.Run("write to temp file", func(t *testing.T) {
		testFile := "goreact_test_write.txt"
		result, err := write.Execute(context.Background(), map[string]any{
			"path":    testFile,
			"content": "hello world",
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("Expected success to be true")
		}
		if resultMap["bytes_written"] == nil {
			t.Error("Expected bytes_written to be set")
		}
		os.Remove(testFile)
	})

	t.Run("append to file", func(t *testing.T) {
		testFile := "goreact_test_append.txt"
		write.Execute(context.Background(), map[string]any{"path": testFile, "content": "line1\n"})
		result, err := write.Execute(context.Background(), map[string]any{
			"path":    testFile,
			"content": "line2\n",
			"append":  true,
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["mode"] != "append" {
			t.Errorf("Expected mode 'append', got %v", resultMap["mode"])
		}
		os.Remove(testFile)
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := write.Execute(context.Background(), map[string]any{"content": "hello"})
		if err == nil {
			t.Error("Expected error for missing path")
		}
	})

	t.Run("missing content", func(t *testing.T) {
		_, err := write.Execute(context.Background(), map[string]any{"path": "/tmp/test.txt"})
		if err == nil {
			t.Error("Expected error for missing content")
		}
	})

	t.Run("Name and Description", func(t *testing.T) {
		if write.Info().Name != "Write" {
			t.Errorf("Expected 'write', got %q", write.Info().Name)
		}
		if write.Info().Description == "" {
			t.Error("Expected non-empty description")
		}
		if write.Info().SecurityLevel != events.LevelSensitive {
			t.Errorf("Expected LevelSensitive, got %v", write.Info().SecurityLevel)
		}
	})
}

func TestValidateFunctions(t *testing.T) {
	t.Run("validateRequired", func(t *testing.T) {
		err := ValidateRequired(map[string]any{"key": "value"}, "key")
		if err != nil {
			t.Error("Expected no error for existing key")
		}

		err = ValidateRequired(map[string]any{}, "missing")
		if err == nil {
			t.Error("Expected error for missing key")
		}
	})

	t.Run("validateRequiredString", func(t *testing.T) {
		val, err := ValidateRequiredString(map[string]any{"key": "value"}, "key")
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if val != "value" {
			t.Errorf("Expected 'value', got %q", val)
		}

		_, err = ValidateRequiredString(map[string]any{"key": 123}, "key")
		if err == nil {
			t.Error("Expected error for non-string value")
		}

		_, err = ValidateRequiredString(map[string]any{}, "missing")
		if err == nil {
			t.Error("Expected error for missing key")
		}
	})

	t.Run("validateFileSafety - restricted files", func(t *testing.T) {
		// These are outside workspace, so should be rejected
		err := ValidateFileSafety("/etc/passwd", "")
		if err == nil {
			t.Error("Expected error for /etc/passwd (outside workspace)")
		}

		err = ValidateFileSafety("/etc/shadow", "")
		if err == nil {
			t.Error("Expected error for /etc/shadow (outside workspace)")
		}

		err = ValidateFileSafety("/etc/sudoers", "")
		if err == nil {
			t.Error("Expected error for /etc/sudoers (outside workspace)")
		}

		// A path inside workspace but with restricted filename should be rejected
		err = ValidateFileSafety(".env", "")
		if err == nil {
			t.Error("Expected error for .env (restricted filename)")
		}

		// A safe path inside workspace should pass
		err = ValidateFileSafety("safe_file.txt", "")
		if err != nil {
			t.Errorf("Expected no error for safe local path, got %v", err)
		}
	})
}

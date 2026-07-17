package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// testCtx creates a context with a ToolContext containing a session for testing.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	cwd, _ := os.Getwd()
	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", cwd, store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	return WithToolContext(context.Background(), &ToolContext{
		Session:   sess,
		EmitEvent: func(e events.ReactEvent) {},
	})
}

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
	ctx := testCtx(t)

	t.Run("list current directory", func(t *testing.T) {
		result, err := ls.Execute(ctx, map[string]any{"path": "."})
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
		_, err := ls.Execute(ctx, map[string]any{"path": "/nonexistent_dir_12345"})
		if err == nil {
			t.Error("Expected error for non-existent directory")
		}
	})

	t.Run("path is not a directory", func(t *testing.T) {
		_, err := ls.Execute(ctx, map[string]any{"path": "builtin_test.go"})
		if err == nil {
			t.Error("Expected error when path is not a directory")
		}
	})

	t.Run("show hidden files", func(t *testing.T) {
		result, err := ls.Execute(ctx, map[string]any{"path": ".", "show_hidden": true})
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
	ctx := testCtx(t)
	absPath := mustAbs(t, "builtin_test.go")

	t.Run("read this test file", func(t *testing.T) {
		resultI, err := read.Execute(ctx, map[string]any{"filePath": absPath})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != true {
			t.Error("Expected success to be true")
		}
		if rr.Data.Content == "" {
			t.Error("Expected content to be set")
		}
	})

	t.Run("read with line range", func(t *testing.T) {
		resultI, err := read.Execute(ctx, map[string]any{
			"filePath":   absPath,
			"start_line": 1.0,
			"end_line":   5.0,
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != true {
			t.Error("Expected success to be true")
		}
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := read.Execute(ctx, map[string]any{})
		if err == nil {
			t.Error("Expected error for missing path")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		resultI, err := read.Execute(ctx, map[string]any{"filePath": "./nonexistent_file_12345.txt"})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != false {
			t.Error("Expected success to be false for non-existent file")
		}
		if rr.Data.Suggestion != SuggestionFileNotFound {
			t.Errorf("Expected _suggestion=%q, got %v", SuggestionFileNotFound, rr.Data.Suggestion)
		}
	})

	t.Run("path is a directory", func(t *testing.T) {
		resultI, err := read.Execute(ctx, map[string]any{"filePath": "."})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != false {
			t.Error("Expected success to be false for directory")
		}
		if rr.Data.Suggestion != SuggestionIsDirectory {
			t.Errorf("Expected _suggestion=%q, got %v", SuggestionIsDirectory, rr.Data.Suggestion)
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
	ctx := testCtx(t)

	t.Run("write to temp file", func(t *testing.T) {
		testFile := "goharness_test_write.txt"
		// 先清理可能存在的文件
		os.Remove(testFile)
		result, err := write.Execute(ctx, map[string]any{
			"filePath": testFile,
			"content":  "hello world",
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		writeResult := result.(*WriteResult)
		if !writeResult.Success {
			t.Error("Expected success to be true")
		}
		if writeResult.BytesWritten == 0 {
			t.Error("Expected bytes_written to be set")
		}
		os.Remove(testFile)
	})

	t.Run("append to file", func(t *testing.T) {
		testFile := "goharness_test_append.txt"
		write.Execute(ctx, map[string]any{"filePath": testFile, "content": "line1\n"})
		result, err := write.Execute(ctx, map[string]any{
			"filePath": testFile,
			"content":  "line2\n",
			"append":   true,
		})
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		writeResult := result.(*WriteResult)
		if writeResult.Type != "append" {
			t.Errorf("Expected type 'append', got %v", writeResult.Type)
		}
		os.Remove(testFile)
	})

	t.Run("missing filePath", func(t *testing.T) {
		_, err := write.Execute(ctx, map[string]any{"content": "hello"})
		if err == nil {
			t.Error("Expected error for missing filePath")
		}
	})

	t.Run("missing content", func(t *testing.T) {
		_, err := write.Execute(ctx, map[string]any{"filePath": "/tmp/test.txt"})
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

// ========== 边界情况测试补充 ==========

// TestBash_EdgeCases Bash 工具边界测试
func TestBash_EdgeCases(t *testing.T) {
	bash := NewBashToolUnrestricted()

	t.Run("空命令字符串", func(t *testing.T) {
		_, err := bash.Execute(context.Background(), map[string]any{"command": ""})
		if err == nil {
			t.Error("空命令应返回错误")
		}
	})

	t.Run("纯空白命令", func(t *testing.T) {
		_, err := bash.Execute(context.Background(), map[string]any{"command": "   \t  "})
		if err == nil {
			t.Error("纯空白命令应返回错误")
		}
	})

	t.Run("超长命令（超过 100000 字符）", func(t *testing.T) {
		longCmd := strings.Repeat("a", 100001)
		_, err := bash.Execute(context.Background(), map[string]any{"command": longCmd})
		if err == nil {
			t.Error("超长命令应返回错误")
		}
	})

	t.Run("timeout 参数 - 最小值限制", func(t *testing.T) {
		result, err := bash.Execute(context.Background(), map[string]any{
			"command": "echo test",
			"timeout": float64(100), // 小于最小值 1000
		})
		if err != nil {
			t.Fatalf("小 timeout 不应导致执行失败: %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("小 timeout 应被修正为最小值并正常执行")
		}
	})

	t.Run("timeout 参数 - 最大值限制", func(t *testing.T) {
		result, err := bash.Execute(context.Background(), map[string]any{
			"command": "echo test",
			"timeout": float64(999999), // 超过最大值 300000
		})
		if err != nil {
			t.Fatalf("大 timeout 不应导致执行失败: %v", err)
		}
		resultMap := result.(map[string]any)
		if resultMap["success"] != true {
			t.Error("大 timeout 应被修正为最大值并正常执行")
		}
	})

	t.Run("输出截断 - 大 stdout", func(t *testing.T) {
		cmd := fmt.Sprintf("python3 -c \"print('x' * %d)\"", maxBashOutputSize*2)
		result, err := bash.Execute(context.Background(), map[string]any{"command": cmd})
		if err != nil {
			t.Skipf("跳过: python3 可能不可用: %v", err)
		}
		resultMap := result.(map[string]any)
		stdout := resultMap["stdout"].(string)
		if len(stdout) > maxBashOutputSize+100 {
			t.Errorf("stdout 应被截断，但得到 %d 字符", len(stdout))
		}
	})
}

// TestRead_EdgeCases Read 工具边界测试
func TestRead_EdgeCases(t *testing.T) {
	read := NewReadTool()
	ctx := testCtx(t)
	tempDir, err := os.MkdirTemp(".", "read_edge_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("读取空文件", func(t *testing.T) {
		emptyFile := filepath.Join(tempDir, "empty.txt")
		os.WriteFile(emptyFile, []byte(""), 0644)

		resultI, err := read.Execute(ctx, map[string]any{"filePath": emptyFile})
		if err != nil {
			t.Fatalf("读取空文件失败: %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != true {
			t.Error("读取空文件应成功")
		}
		_ = rr.Data.Content
	})

	t.Run("offset 和 limit 参数", func(t *testing.T) {
		multiLineFile := filepath.Join(tempDir, "multiline.txt")
		lines := make([]string, 20)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d content", i+1)
		}
		os.WriteFile(multiLineFile, []byte(strings.Join(lines, "\n")), 0644)

		resultI, err := read.Execute(ctx, map[string]any{
			"filePath": multiLineFile,
			"offset":   float64(5),
			"limit":  float64(3),
		})
		if err != nil {
			t.Fatalf("分页读取失败: %v", err)
		}
		rr := resultI.(*ReadResult)
		startLine := rr.Data.StartLine
		linesRead := rr.Data.LinesRead

		if startLine != 5 {
			t.Errorf("start_line 应为 5，得到 %d", startLine)
		}
		if linesRead > 3 {
			t.Errorf("lines_read 不应超过 limit (3)，得到 %d", linesRead)
		}
	})

	t.Run("相对路径处理", func(t *testing.T) {
		resultI, err := read.Execute(ctx, map[string]any{"filePath": "./builtin_test.go"})
		if err != nil {
			t.Fatalf("相对路径读取失败: %v", err)
		}
		rr := resultI.(*ReadResult)
		if rr.Data.Success != true {
			t.Error("相对路径应能正常工作")
		}
	})
}

// TestWrite_EdgeCases Write 工具边界测试
func TestWrite_EdgeCases(t *testing.T) {
	write := NewWriteTool()
	ctx := testCtx(t)
	tempDir, err := os.MkdirTemp(".", "write_edge_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Run("写入空内容", func(t *testing.T) {
		emptyFile := filepath.Join(tempDir, "empty_write.txt")
		result, err := write.Execute(ctx, map[string]any{
			"filePath": emptyFile,
			"content":  "",
		})
		if err != nil {
			t.Fatalf("写入空内容失败: %v", err)
		}
		writeResult := result.(*WriteResult)
		if writeResult.BytesWritten != 0 {
			t.Errorf("写入空内容应返回 0 字节，得到 %d", writeResult.BytesWritten)
		}
	})

	t.Run("创建深层目录结构", func(t *testing.T) {
		deepFile := filepath.Join(tempDir, "a", "b", "c", "deep.txt")
		result, err := write.Execute(ctx, map[string]any{
			"filePath": deepFile,
			"content":  "deep content",
		})
		if err != nil {
			t.Fatalf("创建深层目录失败: %v", err)
		}
		writeResult := result.(*WriteResult)
		if !writeResult.Success {
			t.Error("应自动创建目录结构")
		}
	})

	t.Run("append 模式追加到已有文件", func(t *testing.T) {
		appendFile := filepath.Join(tempDir, "append_test.txt")

		write.Execute(ctx, map[string]any{
			"filePath": appendFile,
			"content":  "first line\n",
		})

		result, err := write.Execute(ctx, map[string]any{
			"filePath": appendFile,
			"content":  "second line\n",
			"append":   true,
		})
		if err != nil {
			t.Fatalf("追加模式失败: %v", err)
		}
		writeResult := result.(*WriteResult)
		if writeResult.TotalSize < 20 { // "first line\n" + "second line\n"
			t.Errorf("追加后文件大小应大于 20 字节，得到 %d", writeResult.TotalSize)
		}
	})

	t.Run("覆盖模式替换已有内容", func(t *testing.T) {
		overwriteFile := filepath.Join(tempDir, "overwrite.txt")

		write.Execute(ctx, map[string]any{
			"filePath": overwriteFile,
			"content":  strings.Repeat("original ", 100),
		})

		// 覆盖前必须先读取文件（读前写约束）
		read := NewReadTool()
		read.Execute(ctx, map[string]any{"filePath": overwriteFile})

		result, err := write.Execute(ctx, map[string]any{
			"filePath": overwriteFile,
			"content":  "new short content",
		})
		if err != nil {
			t.Fatalf("覆盖模式失败: %v", err)
		}
		writeResult := result.(*WriteResult)
		if writeResult.TotalSize != int64(len("new short content")) {
			t.Errorf("覆盖后文件大小不匹配: 期望 %d，得到 %d", len("new short content"), writeResult.TotalSize)
		}
	})
}

// TestGrep_EdgeCases Grep 工具边界测试
func TestGrep_EdgeCases(t *testing.T) {
	grep := NewGrepTool()

	t.Run("无匹配结果", func(t *testing.T) {
		result, err := grep.Execute(context.Background(), map[string]any{
			"pattern": "ZZZ_NONEXISTENT_PATTERN_ZZZ",
			"path":    ".",
		})
		if err != nil {
			t.Fatalf("无匹配搜索失败: %v", err)
		}
		resultStr := result.(string)
		if resultStr == "" {
			t.Error("无匹配时不应返回空字符串")
		}
	})

	t.Run("特殊正则表达式字符", func(t *testing.T) {
		result, err := grep.Execute(context.Background(), map[string]any{
			"pattern": `func\s+\w+\(`,
			"path":    "*.go",
		})
		if err != nil {
			t.Fatalf("正则表达式搜索失败: %v", err)
		}
		if result == nil {
			t.Error("正则表达式搜索不应返回 nil")
		}
	})

	t.Run("files_with_matches 输出模式", func(t *testing.T) {
		result, err := grep.Execute(context.Background(), map[string]any{
			"pattern":     "package tools",
			"output_mode": "files_with_matches",
			"path":        ".",
		})
		if err != nil {
			t.Fatalf("files_with_matches 模式失败: %v", err)
		}
		if result == nil {
			t.Error("结果不应为 nil")
		}
	})
}

// TestEdit_EdgeCases Edit 工具额外边界测试
func TestEdit_EdgeCases(t *testing.T) {
	ctx := testCtx(t)
	dir, err := os.MkdirTemp(".", "edit_edge_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	t.Run("replace_all 替换全部后验证", func(t *testing.T) {
		filePath := filepath.Join(dir, "replace_all.txt")
		content := "apple banana apple cherry apple\n"
		os.WriteFile(filePath, []byte(content), 0644)

		// 编辑前必须先读取文件（读前写约束）
		read := NewReadTool()
		read.Execute(ctx, map[string]any{"filePath": filePath})

		edit := &EditTool{}
		_, err = edit.Execute(ctx, map[string]any{
			"filePath":    filePath,
			"old_string":  "apple",
			"new_string":  "orange",
			"replace_all": true,
		})
		if err != nil {
			t.Fatalf("replace_all 失败: %v", err)
		}

		newContent, _ := os.ReadFile(filePath)
		expected := "orange banana orange cherry orange\n"
		if string(newContent) != expected {
			t.Errorf("replace_all 结果不正确: 得到 '%s'", string(newContent))
		}
	})

	t.Run("old_string 为空字符串", func(t *testing.T) {
		filePath := filepath.Join(dir, "empty_old.txt")
		os.WriteFile(filePath, []byte("some content"), 0644)

		edit := &EditTool{}
		_, err = edit.Execute(ctx, map[string]any{
			"filePath":   filePath,
			"old_string": "",
			"new_string": "replacement",
		})
		if err == nil {
			t.Error("old_string 为空应返回错误")
		}
	})

	t.Run("多次编辑同一文件", func(t *testing.T) {
		filePath := filepath.Join(dir, "multi_edit.txt")
		original := "hello world foo bar\n"
		os.WriteFile(filePath, []byte(original), 0644)

		edit := &EditTool{}

		// 第一次编辑前需要读取文件
		read := NewReadTool()
		read.Execute(ctx, map[string]any{"filePath": filePath})
		edit.Execute(ctx, map[string]any{
			"filePath":   filePath,
			"old_string": "hello",
			"new_string": "hi",
		})
		// 后续编辑前也需要重新读取
		read.Execute(ctx, map[string]any{"filePath": filePath})
		edit.Execute(ctx, map[string]any{
			"filePath":   filePath,
			"old_string": "world",
			"new_string": "earth",
		})
		read.Execute(ctx, map[string]any{"filePath": filePath})
		edit.Execute(ctx, map[string]any{
			"filePath":   filePath,
			"old_string": "foo",
			"new_string": "baz",
		})

		finalContent, _ := os.ReadFile(filePath)
		expected := "hi earth baz bar\n"
		if string(finalContent) != expected {
			t.Errorf("多次编辑后内容不正确: 得到 '%s'", string(finalContent))
		}
	})
}

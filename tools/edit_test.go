package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdit(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "test.txt")
	content := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":   filePath,
		"old_string": "line 2",
		"new_string": "line 2 replaced",
	}

	result, err := edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("replace failed: %v", err)
	}

	editResult, ok := result.(*EditResult)
	if !ok {
		t.Fatalf("expected *EditResult, got %T", result)
	}
	if !editResult.Success {
		t.Error("expected success to be true")
	}

	newContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	want := "line 1\nline 2 replaced\nline 3\n"
	if string(newContent) != want {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), want)
	}

	// 第二次编辑无需重新读取：Edit 成功后会自动更新已读状态
	params2 := map[string]any{
		"filePath":   filePath,
		"old_string": "line 1",
		"new_string": "first line",
	}
	_, err = edit.Execute(ctx, params2)
	if err != nil {
		t.Fatalf("second replace failed: %v", err)
	}

	newContent2, _ := os.ReadFile(filePath)
	want2 := "first line\nline 2 replaced\nline 3\n"
	if string(newContent2) != want2 {
		t.Errorf("unexpected content after second replace: %q", string(newContent2))
	}
}

// TestEditConsecutiveWithoutReRead 验证同一文件的连续多次 Edit 无需在中间重新 Read。
// 回归：修复前 Edit 成功后清除 StaleState 导致第二次 Edit 被"未读取"拒绝，
// 迫使模型反复 Read→Edit→Read→Edit，浪费大量 Token 与调用次数。
func TestEditConsecutiveWithoutReRead(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_cont_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "test.txt")
	content := "line 1\nline 2\nline 3\nline 4\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 首次编辑前先读取一次
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	// 连续两次编辑不同段落，第二次之前不再 Read
	if _, err := edit.Execute(ctx, map[string]any{
		"filePath": filePath, "old_string": "line 2", "new_string": "line 2 replaced",
	}); err != nil {
		t.Fatalf("第一次编辑失败: %v", err)
	}
	if _, err := edit.Execute(ctx, map[string]any{
		"filePath": filePath, "old_string": "line 3", "new_string": "line 3 replaced",
	}); err != nil {
		t.Fatalf("第二次编辑（未重新 Read）失败: %v", err)
	}

	newContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	want := "line 1\nline 2 replaced\nline 3 replaced\nline 4\n"
	if string(newContent) != want {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), want)
	}
}

// TestEditRejectsExternalModification 验证 staleness 安全防护仍有效：
// 文件在 Read 之后被外部修改（如用户手动编辑），Edit 必须拒绝并提示重新读取。
// 与连续编辑（TestEditConsecutiveWithoutReRead）构成互补，防止修复破坏安全防护。
func TestEditRejectsExternalModification(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_extmod_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "test.txt")
	content := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 先读取，建立已读状态
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	// 模拟外部修改：文件内容被用户/其他进程更改
	// 等待数毫秒确保 mtime 与首次创建时刻不同，避免同毫秒写入导致漏检
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("line 1\nline 2 changed externally\nline 3\n"), 0644); err != nil {
		t.Fatalf("failed to modify file externally: %v", err)
	}

	_, err = edit.Execute(ctx, map[string]any{
		"filePath": filePath, "old_string": "line 2", "new_string": "line 2 replaced",
	})
	if err == nil {
		t.Fatal("期望外部修改后被拒绝，但 Edit 成功执行了")
	}
	if !strings.Contains(err.Error(), "外部修改") {
		t.Errorf("错误信息应提示文件已被外部修改，got: %v", err)
	}
}

func TestEditFileNotFound(t *testing.T) {
	edit := &EditTool{}
	ctx := testCtx(t)
	params := map[string]any{
		"filePath":   "./nonexistent/file.txt",
		"old_string": "something",
		"new_string": "something else",
	}

	_, err := edit.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestEditWithSpecialCharacters(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "special.txt")
	content := "Hello <world> & {foo}\nline with \"quotes\" and tabs\t\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":   filePath,
		"old_string": "<world> & {foo}",
		"new_string": "<planet> | {bar}",
	}

	_, err = edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("replace with special chars failed: %v", err)
	}

	newContent, _ := os.ReadFile(filePath)
	expected := "Hello <planet> | {bar}\nline with \"quotes\" and tabs\t\n"
	if string(newContent) != expected {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), expected)
	}
}

func TestEditUnicodeContent(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "unicode.txt")
	content := "Hello 世界\nこんにちは\n🌍\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":   filePath,
		"old_string": "世界",
		"new_string": "宇宙",
	}

	_, err = edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("unicode replace failed: %v", err)
	}

	newContent, _ := os.ReadFile(filePath)
	expected := "Hello 宇宙\nこんにちは\n🌍\n"
	if string(newContent) != expected {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), expected)
	}
}

func TestEditEmptyFile(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(filePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)
	params := map[string]any{
		"filePath":   filePath,
		"old_string": "nonexistent",
		"new_string": "something",
	}

	_, err = edit.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error for empty file with no match")
	}
}

func TestEditReplaceAll(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "multi.txt")
	content := "foo bar foo bar foo\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":    filePath,
		"old_string":  "foo",
		"new_string":  "baz",
		"replace_all": true,
	}

	_, err = edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("replace all failed: %v", err)
	}

	newContent, _ := os.ReadFile(filePath)
	expected := "baz bar baz bar baz\n"
	if string(newContent) != expected {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), expected)
	}
}

func TestEditLimit(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "limit.txt")
	content := "x y x y x\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":   filePath,
		"old_string": "x",
		"new_string": "z",
		"limit":      2.0,
	}

	_, err = edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("replace with limit failed: %v", err)
	}

	newContent, _ := os.ReadFile(filePath)
	expected := "z y z y x\n"
	if string(newContent) != expected {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), expected)
	}
}

func TestEditStringNotFound(t *testing.T) {
	dir, err := os.MkdirTemp(".", "edit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "nomatch.txt")
	content := "hello world\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	edit := &EditTool{}
	ctx := testCtx(t)

	// 编辑前必须先读取文件
	read := NewReadTool()
	read.Execute(ctx, map[string]any{"filePath": filePath})

	params := map[string]any{
		"filePath":   filePath,
		"old_string": "nonexistent",
		"new_string": "something",
	}

	_, err = edit.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when old_string not found")
	}
}

func TestEditMissingPath(t *testing.T) {
	edit := &EditTool{}
	ctx := testCtx(t)
	params := map[string]any{
		"old_string": "foo",
		"new_string": "bar",
	}

	_, err := edit.Execute(ctx, params)
	if err == nil {
		t.Fatal("expected error when path is missing")
	}
}

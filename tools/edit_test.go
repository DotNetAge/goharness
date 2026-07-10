package tools

import (
	"os"
	"path/filepath"
	"testing"
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
	params := map[string]any{
		"filePath":   filePath,
		"old_string": "line 2",
		"new_string": "line 2 replaced",
	}

	result, err := edit.Execute(ctx, params)
	if err != nil {
		t.Fatalf("replace failed: %v", err)
	}

	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", result)
	}
	cwd, _ := os.Getwd()
	absPath := filepath.Join(cwd, filePath)
	expected := "File " + absPath + " updated successfully. [scope: project]"
	if str != expected {
		t.Errorf("unexpected result: %q", str)
	}

	newContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	want := "line 1\nline 2 replaced\nline 3\n"
	if string(newContent) != want {
		t.Errorf("unexpected content: got %q, want %q", string(newContent), want)
	}

	// second replace on same file (no error expected)
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

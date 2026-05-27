package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// FileEditTool implements a tool for editing files with staleness checks.
type FileEditTool struct{}

func NewFileEditTool() FuncTool {
	return &FileEditTool{}
}

func (t *FileEditTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "FileEdit",
		Description: "Edit files by replacing exact strings. Supports single, all, or limited-count replacements with staleness check.",
		Prompt: `Performs exact string replacements in files.

Usage:
- You must use your Read tool at least once in the conversation before editing. This tool will error if you attempt an edit without reading the file.
- When editing text from Read tool output, ensure you preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix.
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- The edit will FAIL if old_string is not found in the file, or if it appears multiple times without replace_all=true or limit set.
- Use replace_all=true to rename a variable or change a string everywhere in the file.
- Use limit=N to replace the first N occurrences only (e.g. limit=2 replaces the first 2 matches).
- Use last_read_time to prevent stale writes — pass the file's modification timestamp from your last Read result.`,
		Tags: []string{"file", "edit", "code", "replace", "modification"},
		Parameters: []Parameter{
			{
				Name:        "path",
				Type:        "string",
				Description: "Path to the file to edit.",
				Required:    true,
			},
			{
				Name:        "old_string",
				Type:        "string",
				Description: "The exact string to replace.",
				Required:    true,
			},
			{
				Name:        "new_string",
				Type:        "string",
				Description: "The new string to insert.",
				Required:    true,
			},
			{
				Name:        "replace_all",
				Type:        "boolean",
				Description: "Replace all occurrences. Default: false (replaces first occurrence only).",
				Required:    false,
			},
			{
				Name:        "limit",
				Type:        "number",
				Description: "Replace at most N occurrences (overrides replace_all when set). -1 = all.",
				Required:    false,
			},
			{
				Name:        "last_read_time",
				Type:        "string",
				Description: "File modification timestamp from Read result. Prevents editing a stale version.",
				Required:    false,
			},
		},
	}
}

func (t *FileEditTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	filePath, err := ValidateRequiredString(params, "path")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(filePath, tc.ProjectDir, tc.SessionDir)

	logger.Info("editing file",
		"input_path", filePath,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	// Security check
	if err := ValidateFileSafety(resolvedPath, tc.ProjectDir); err != nil {
		return nil, err
	}

	oldStr, err := ValidateRequiredString(params, "old_string")
	if err != nil {
		return nil, err
	}

	newStr, err := ValidateRequiredString(params, "new_string")
	if err != nil {
		return nil, err
	}

	replaceAll, _ := params["replace_all"].(bool)
	lastReadTimeStr, _ := params["last_read_time"].(string)

	// Parse optional limit (-1 = all, 0 = default, N = at most N occurrences)
	var limit int
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
	}

	// Staleness check
	if lastReadTimeStr != "" {
		info, err := os.Stat(resolvedPath)
		if err == nil {
			lastReadTime, parseErr := time.Parse(time.RFC3339, lastReadTimeStr)
			if parseErr == nil && info.ModTime().After(lastReadTime) {
				return nil, fmt.Errorf("file has been modified since it was last read. please read it again before editing")
			}
		}
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}

	fileContent := string(content)
	if oldStr == "" {
		return nil, fmt.Errorf("old_string must not be empty")
	}
	if !strings.Contains(fileContent, oldStr) {
		return nil, fmt.Errorf("old_string not found in file")
	}
	if !replaceAll && limit <= 0 && strings.Count(fileContent, oldStr) > 1 {
		return nil, fmt.Errorf("old_string appears %d times in file. Use replace_all=true or limit=N to replace multiple occurrences",
			strings.Count(fileContent, oldStr))
	}

	var updatedContent string
	switch {
	case limit < -1:
		return nil, fmt.Errorf("limit must be -1 (all), 0 (default 1), or positive")
	case limit == -1:
		updatedContent = strings.ReplaceAll(fileContent, oldStr, newStr)
	case limit > 0:
		updatedContent = strings.Replace(fileContent, oldStr, newStr, limit)
	case replaceAll:
		updatedContent = strings.ReplaceAll(fileContent, oldStr, newStr)
	default:
		updatedContent = strings.Replace(fileContent, oldStr, newStr, 1)
	}

	err = os.WriteFile(resolvedPath, []byte(updatedContent), 0644)
	if err != nil {
		return nil, err
	}

	return fmt.Sprintf("File %s updated successfully. [scope: %s]", resolvedPath, scope), nil
}

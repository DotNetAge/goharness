package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// EditTool 实现了文件编辑工具。
// 支持通过精确字符串匹配进行文件内容替换，具有以下特性：
//   - 精确匹配：必须完全匹配 old_string 才会替换
//   - 多种替换模式：单次、全部、限制次数
//   - 过时检测：通过 last_read_time 防止基于过期内容的编辑
//   - 安全检查：路径验证和参数验证
//
// 适用场景：
//   - 修改已有文件的小部分内容
//   - 变量重命名（使用 replace_all）
//   - 代码重构（使用 limit 控制范围）
type EditTool struct{}

// NewEditTool 创建一个 EditTool 实例。
//
// 返回：
//   - FuncTool: 配置好的 EditTool 实例
func NewEditTool() FuncTool {
	return &EditTool{}
}

// Info 返回 EditTool 的元信息。
func (t *EditTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "Edit",
		Description: "Edit files by replacing exact strings.",
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

// Grant implements tools.PermissionRequired. Symmetric with Write's Grant:
// the only ask-the-user signal is "the resolved target file is outside the
// workspace boundary". Everything else (file not found, old_string missing,
// stale modification time, etc.) is a normal Execute-level error.
func (t *EditTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	filePath, _ := params["path"].(string)
	if filePath == "" {
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return true, ""
	}

	resolved, _ := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())
	if resolved == "" {
		return true, ""
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		return false, fmt.Sprintf(
			"Edit on %q resolves to %q which is outside the workspace.\n%s",
			filePath, resolved, err.Error(),
		)
	}
	return true, ""
}

// Execute 执行文件编辑操作。
//
// 处理流程：
//  1. 验证必需参数（path, old_string, new_string）
//  2. 解析并验证目标路径
//  3. 安全性检查（路径不能超出项目目录）
//  4. 过时检测（如果提供了 last_read_time）
//  5. 读取文件内容
//  6. 验证 old_string 存在且匹配规则正确
//  7. 执行替换操作（根据 replace_all 和 limit 参数）
//  8. 写入更新后的内容
//
// 替换模式：
//   - limit < -1: 错误
//   - limit == -1 或 replace_all=true: 替换所有
//   - limit > 0: 替换前 N 次
//   - 默认: 只替换第一次
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 path, old_string, new_string；可选 replace_all, limit, last_read_time
//
// 返回：
//   - string: 成功消息，包含文件路径和作用域
//   - error: 参数错误、验证失败或 I/O 错误
func (t *EditTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	filePath, err := ValidateRequiredString(params, "path")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())

	logger.Info("editing file",
		"input_path", filePath,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	// Security check: block sensitive system files.
	// Workspace boundary enforcement is handled by FileBoundaryChecker
	// at the permission chain level (executor).
	if err := checkSensitiveFiles(resolvedPath); err != nil {
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

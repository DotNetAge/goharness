package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/DotNetAge/goharness/tools/filestate"
)

// EditTool 实现了文件编辑工具。
//
//   - 容错匹配：花引号降级 + 末尾换行容错（B 节）
//   - Staleness 自动检测：通过 filestate.CheckStale（C 节）
//   - 结构化输出：返回 EditResult（D 节）
//   - 创建语义：old_string="" + 空文件时写入新内容（E 节）
//   - ENOENT + old_string="" 简单拒绝：不会静默创建（审计 C2 简化）
//
// 替换模式：
//   - limit < -1: 错误
//   - limit == -1 或 replace_all=true: 替换所有
//   - limit > 0: 替换前 N 次
//   - 默认: 只替换第一次
type EditTool struct {
	whitelist []string
}

// AddWhiteList 添加允许编辑的目录前缀。
func (t *EditTool) AddWhiteList(dirs ...string) *EditTool {
	t.whitelist = append(t.whitelist, dirs...)
	return t
}

// NewEditTool 创建一个 EditTool 实例。
func NewEditTool() *EditTool {
	return &EditTool{}
}

// Info 返回 EditTool 的元信息。
func (t *EditTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "Edit",
		Description: "通过精确字符串替换来编辑文件。支持花引号降级和末尾换行容错。",
		Prompt: `在文件中进行精确字符串替换。
**用法**
- 编辑 Read 工具输出中的文本时，确保保留行号前缀之后的精确缩进（制表符/空格）。
- old_string 必须精确匹配文件中的内容。工具会自动处理花引号和末尾换行差异。
- 使用 replace_all=true 在文件所有位置更改字符串。
- 使用 limit=N 仅替换前 N 次出现。
- 使用 old_string="" + 空文件来创建新文件。非空文件使用 Write 工具。`,
		Tags: []string{"file", "edit", "code", "replace", "modification"},
		Parameters: []Parameter{
			{
				Name:        "file_path",
				Type:        "string",
				Description: "要编辑的文件路径。",
				Required:    true,
			},
			{
				Name:        "old_string",
				Type:        "string",
				Description: "要被替换的精确字符串。为空且文件不存在时创建新文件。",
				Required:    true,
			},
			{
				Name:        "new_string",
				Type:        "string",
				Description: "要插入的新字符串。",
				Required:    true,
			},
			{
				Name:        "replace_all",
				Type:        "boolean",
				Description: "替换所有出现。默认值：false（仅替换第一次出现）。",
				Required:    false,
			},
			{
				Name:        "limit",
				Type:        "number",
				Description: "最多替换 N 次出现（设置时覆盖 replace_all）。-1 = 全部。",
				Required:    false,
			},
		},
	}
}

// Grant implements tools.PermissionRequired.
func (t *EditTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	raw, _ := GetParam(params, "file_path")
	filePath, _ := raw.(string)
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
		for _, dir := range t.whitelist {
			if strings.HasPrefix(resolved, dir) {
				return true, ""
			}
		}
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Edit {
				if strings.HasPrefix(resolved, allowed) {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf(
			"编辑 %q 解析为 %q，这在工作区之外。\n%s",
			filePath, resolved, err.Error(),
		)
	}
	return true, ""
}

// Execute 执行文件编辑操作。
func (t *EditTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	filePath, err := ValidateRequiredString(params, "file_path")
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

	// Security check
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

	rawReplaceAll, _ := GetParam(params, "replace_all")
	replaceAll, _ := rawReplaceAll.(bool)

	var limit int
	if raw, found := GetParam(params, "limit"); found {
		if l, ok := raw.(float64); ok {
			limit = int(l)
		}
	}

	// E. 创建 + ENOENT 合并处理
	info, statErr := os.Stat(resolvedPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			if oldStr == "" {
				// ENOENT + old_string="" → 简单拒绝（审计 C2 简化）
				return nil, fmt.Errorf(
					"文件 %s 不存在。如需创建文件请使用 Write 工具。",
					resolvedPath,
				)
			}
			// ENOENT + old_string!="" → CWD 前缀建议
			return nil, fmt.Errorf(
				"路径 %s 在当前工作区内不存在。"+
					"请检查路径是否拼写正确。"+
					"您可以使用 Glob 或 ls 工具查找文件。"+
					"如果文件未被创建，使用 Write 工具创建它。",
				resolvedPath,
			)
		}
		return nil, fmt.Errorf("无法访问文件 %s: %w", resolvedPath, err)
	}

	// 存在但为空 + old_string="" → 创建模式
	if info.Size() == 0 && oldStr == "" {
		dir := filepath.Dir(resolvedPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %w", err)
		}
		if err := os.WriteFile(resolvedPath, []byte(newStr), 0644); err != nil {
			return nil, fmt.Errorf("写入文件失败: %w", err)
		}
		// 写入后清除 StaleState 和 NegativeCache
		filestate.DeleteStale(resolvedPath)
		invalidateNegativeCache(resolvedPath)
		return &EditResult{
			Success:      true,
			FilePath:     resolvedPath,
			Scope:        string(scope),
			ReplaceCount: 1,
			ReplaceMode:  "create",
		}, nil
	}

	// 存在且非空 + old_string="" → 拒绝
	if oldStr == "" {
		return nil, fmt.Errorf(
			"old_string 为空，但文件 %s 已有内容。"+
				"请明确指定要替换的内容。使用 Write 工具可完全覆盖文件。",
			resolvedPath,
		)
	}

	// C. Staleness 自动检测
	if err := filestate.CheckStale(resolvedPath); err != nil {
		return nil, err
	}

	// 读取文件内容
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	fileContent := string(content)

	// B. 容错匹配
	actualOld, found := findEditMatch(fileContent, oldStr)
	if !found {
		return nil, fmt.Errorf("old_string 在文件中未找到。请确保 old_string 与文件中的内容完全相同。")
	}

	// 检查替换模式
	totalMatches := strings.Count(fileContent, actualOld)
	if !replaceAll && limit <= 0 && totalMatches > 1 {
		return nil, fmt.Errorf(
			"old_string 在文件中出现了 %d 次。使用 replace_all=true 或 limit=N 替换多次出现",
			totalMatches,
		)
	}

	if limit < -1 {
		return nil, fmt.Errorf("limit 必须为 -1（全部）、0（默认 1）或正数")
	}

	// 执行替换
	var updatedContent string
	var replaceMode string
	var replaceCount int

	switch {
	case limit == -1:
		updatedContent = strings.ReplaceAll(fileContent, actualOld, newStr)
		replaceCount = strings.Count(updatedContent, newStr) // approximate
		replaceMode = "all"
	case limit > 0:
		updatedContent = strings.Replace(fileContent, actualOld, newStr, limit)
		replaceCount = limit
		if replaceCount > totalMatches {
			replaceCount = totalMatches
		}
		replaceMode = fmt.Sprintf("limit:%d", limit)
	case replaceAll:
		updatedContent = strings.ReplaceAll(fileContent, actualOld, newStr)
		replaceCount = totalMatches
		replaceMode = "all"
	default:
		updatedContent = strings.Replace(fileContent, actualOld, newStr, 1)
		replaceCount = 1
		replaceMode = "single"
	}

	// 确保目录存在
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建目录失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0644); err != nil {
		return nil, fmt.Errorf("写入文件 %s 失败: %w", resolvedPath, err)
	}

	// 清除 StaleState，确保后续操作是"未读"状态
	filestate.DeleteStale(resolvedPath)
	invalidateNegativeCache(resolvedPath)

	logger.Info("file edited",
		"resolved_path", resolvedPath,
		"old_len", len(oldStr),
		"new_len", len(newStr),
		"replace_mode", replaceMode,
		"replace_count", replaceCount,
	)

	return &EditResult{
		Success:      true,
		FilePath:     resolvedPath,
		Scope:        string(scope),
		ReplaceCount: replaceCount,
		TotalMatches: totalMatches,
		ReplaceMode:  replaceMode,
	}, nil
}

// findEditMatch 在文件内容中查找 old_string 的匹配。
// 实现 EDIT_DESIGN.md B4 节的 3 层降级匹配。
//
// 第 1 层：精确匹配
// 第 2 层：花引号降级（仅匹配定位，不改变替换语义）
// 第 3 层：末尾换行容错
func findEditMatch(fileContent, oldStr string) (actualOld string, ok bool) {
	// 第 1 层：精确匹配
	if strings.Contains(fileContent, oldStr) {
		return oldStr, true
	}

	// 第 2 层：花引号降级（见过度设计审计 #2：内联 rune 定位）
	normalizedFile := normalizeQuotes(fileContent)
	normalizedOld := normalizeQuotes(oldStr)
	if strings.Contains(normalizedFile, normalizedOld) {
		idx := strings.Index(normalizedFile, normalizedOld)
		fileRunes := []rune(fileContent)
		start := utf8.RuneCountInString(normalizedFile[:idx])
		actualOld = string(fileRunes[start : start+utf8.RuneCountInString(oldStr)])
		return actualOld, true
	}

	// 第 3 层：末尾换行容错
	if !strings.HasSuffix(oldStr, "\n") {
		if strings.Contains(fileContent, oldStr+"\n") {
			return oldStr + "\n", true
		}
		// 换行容错 + 花引号降级组合
		if strings.Contains(normalizedFile, normalizedOld+"\n") {
			idx := strings.Index(normalizedFile, normalizedOld+"\n")
			fileRunes := []rune(fileContent)
			start := utf8.RuneCountInString(normalizedFile[:idx])
			actualOld = string(fileRunes[start : start+utf8.RuneCountInString(oldStr+"\n")])
			return actualOld, true
		}
	}

	return "", false
}

// normalizeQuotes 将字符串中的花引号替换为普通引号。
// 花引号范围：U+201C-U+201F（左右双/单引号）
func normalizeQuotes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u201C', '\u201D', '\u201E', '\u201F':
			b.WriteRune('"')
		case '\u2018', '\u2019', '\u201A', '\u201B':
			b.WriteRune('\'')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// UnixMilli returns the file modification time in milliseconds.
func fileMtimeMs(info os.FileInfo) int64 {
	return info.ModTime().UnixMilli()
}

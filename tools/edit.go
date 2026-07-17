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
type EditTool struct {
	whitelist []string
}

// AddWhiteList 添加允许编辑的目录前缀。
// 当目标路径匹配任一白名单前缀时，Grant() 会直接放行而无需用户确认。
// 通常在工具初始化后、注册到 ToolRegistry 之前调用。
func (t *EditTool) AddWhiteList(dirs ...string) *EditTool {
	t.whitelist = append(t.whitelist, dirs...)
	return t
}

// NewEditTool 创建一个 EditTool 实例。
//
// 返回：
//   - FuncTool: 配置好的 EditTool 实例
func NewEditTool() *EditTool {
	return &EditTool{}
}

// Info 返回 EditTool 的元信息。
func (t *EditTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "Edit",
		Description: "通过精确字符串替换来编辑文件。",
		Prompt: `在文件中进行精确字符串替换。
**用法**
- 编辑 Read 工具输出中的文本时，确保保留行号前缀之后的精确缩进（制表符/空格）。
- 如果 old_string 在文件中未找到，或出现多次但未设置 replace_all=true 或 limit，编辑将失败。
- 使用 replace_all=true 在文件所有位置更改字符串。
- 使用 limit=N 仅替换前 N 次出现。
- 使用 last_read_time 传入上次 Read 结果中的修改时间戳，防止过时写入。`,
		Tags: []string{"file", "edit", "code", "replace", "modification"},
		Parameters: []Parameter{
			{
				Name:        "filePath",
				Type:        "string",
				Description: "要编辑的文件路径。",
				Required:    true,
			},
			{
				Name:        "old_string",
				Type:        "string",
				Description: "要被替换的精确字符串。",
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
			{
				Name:        "last_read_time",
				Type:        "string",
				Description: "来自 Read 结果的文件修改时间戳。防止编辑过时的版本。",
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
		// Check tool-level whitelist first (configured at initialization).
		for _, dir := range t.whitelist {
			if strings.HasPrefix(resolved, dir) {
				return true, ""
			}
		}
		// Then check session whitelist (user "remember my choice").
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
	filePath, err := ValidateRequiredString(params, "filePath")
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

	rawReplaceAll, _ := GetParam(params, "replace_all")
	replaceAll, _ := rawReplaceAll.(bool)
	rawLastReadTime, _ := GetParam(params, "last_read_time")
	lastReadTimeStr, _ := rawLastReadTime.(string)

	// Parse optional limit (-1 = all, 0 = default, N = at most N occurrences)
	var limit int
	if raw, found := GetParam(params, "limit"); found {
		if l, ok := raw.(float64); ok {
			limit = int(l)
		}
	}

	// Staleness check
	if lastReadTimeStr != "" {
		info, err := os.Stat(resolvedPath)
		if err == nil {
			lastReadTime, parseErr := time.Parse(time.RFC3339, lastReadTimeStr)
			if parseErr == nil && info.ModTime().After(lastReadTime) {
				return nil, fmt.Errorf("文件自上次读取后已被修改。请在编辑前重新读取")
			}
		}
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}

	fileContent := string(content)
	if oldStr == "" {
		return nil, fmt.Errorf("old_string 不能为空")
	}
	if !strings.Contains(fileContent, oldStr) {
		return nil, fmt.Errorf("old_string 在文件中未找到")
	}
	if !replaceAll && limit <= 0 && strings.Count(fileContent, oldStr) > 1 {
		return nil, fmt.Errorf("old_string 在文件中出现了 %d 次。使用 replace_all=true 或 limit=N 替换多次出现",
			strings.Count(fileContent, oldStr))
	}

	var updatedContent string
	switch {
	case limit < -1:
		return nil, fmt.Errorf("limit 必须为 -1（全部）、0（默认 1）或正数")
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

	return fmt.Sprintf("文件 %s 更新成功。[scope: %s]", resolvedPath, scope), nil
}

package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/tools/diffutil"
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
		Description: "通过精确字符串替换来编辑文件。",
		Prompt: `在文件中进行精确字符串替换。
**用法**
- old_string 必须精确匹配文件中的内容。编辑 Read 工具输出中的文本时，确保保留行号前缀之后的精确缩进（制表符/空格）。
- 创建新文件或完全重写已有文件使用 Write 工具。

**减少重复调用**
- 同一文件的多个段落修改可连续多次调用本工具，无需在每次调用之间重复 Read：
  首次 Read 的结果持续有效，除非工具明确提示文件已被外部修改。
- 若相同文本需替换多处，优先使用 replace_all 或 limit=N 一次完成，不要逐处调用。`,
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

// Grant 实现 tools.PermissionRequired 接口。
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

	// 沙箱启用时，由沙箱统一做文件安全决策；
	// 未注入沙箱时直接放行，由 Execute 阶段拒绝执行（配置错误，授权无意义）。
	sb := tc.Session.Sandbox()
	if sb == nil {
		return true, ""
	}

	dec := sb.CheckFile(resolved, tc.Session.ProjectDir())
	switch dec.Decision {
	case sandbox.DecisionAllow:
		return true, ""
	case sandbox.DecisionDeny:
		return false, dec.Reason
	case sandbox.DecisionAskUser:
		for _, dir := range t.whitelist {
			if pathWithinScope(dir, resolved) {
				return true, ""
			}
		}
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Edit {
				if pathWithinScope(allowed, resolved) {
					return true, ""
				}
			}
		}
		return false, dec.Reason
	}
	return true, ""
}

// editParams 承载 Edit 工具解析后的参数。
type editParams struct {
	filePath   string
	oldStr     string
	newStr     string
	replaceAll bool
	limit      int
}

// validateEditParams 从参数映射提取并验证 Edit 工具参数。
func validateEditParams(params map[string]any) (editParams, error) {
	filePath, err := ValidateRequiredString("Edit", params, "file_path")
	if err != nil {
		return editParams{}, err
	}
	oldStr, err := ValidateRequiredString("Edit", params, "old_string")
	if err != nil {
		return editParams{}, err
	}
	newStr, err := ValidateRequiredString("Edit", params, "new_string")
	if err != nil {
		return editParams{}, err
	}

	rawReplaceAll, _ := GetParam(params, "replace_all")
	replaceAll, _ := rawReplaceAll.(bool)

	var limit int
	if raw, found := GetParam(params, "limit"); found {
		if l, ok := raw.(float64); ok {
			limit = int(l)
		}
	}
	return editParams{
		filePath:   filePath,
		oldStr:     oldStr,
		newStr:     newStr,
		replaceAll: replaceAll,
		limit:      limit,
	}, nil
}

// authorizeEdit 解析文件路径并执行沙箱强制安全检查。
func (t *EditTool) authorizeEdit(ctx context.Context, filePath string) (resolvedPath string, scope PathScope, err error) {
	tc := GetToolContext(ctx)
	resolvedPath, scope = ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())

	// 安全检查：工作区边界、敏感文件等策略统一由沙箱 EnforceFile 强制检查
	// （含符号链接解析，防 TOCTOU）。未注入沙箱时拒绝执行。
	sb, err := requireSandbox(ctx, "Edit")
	if err != nil {
		return "", "", err
	}
	// 透传工具白名单 + 会话白名单：授权（PermissionAllowSession）与
	// 工具白名单（AddWhiteList）放行的编辑在 Execute 阶段同样豁免边界检查。
	extra := make([]string, 0, len(t.whitelist)+2)
	extra = append(extra, t.whitelist...)
	extra = append(extra, sessionWhitelistDirs(ctx, "edit")...)
	if err := sb.EnforceFileWithWhitelist(resolvedPath, tc.Session.ProjectDir(), extra); err != nil {
		return "", "", err
	}
	return resolvedPath, scope, nil
}

// performEdit 执行文件编辑核心逻辑：存在性检查、ENOENT 处理、创建模式、
// staleness 检测、容错匹配、替换、写入。
func performEdit(logger logging.Logger, resolvedPath string, scope PathScope, p editParams) (*EditResult, error) {
	oldStr, newStr := p.oldStr, p.newStr
	replaceAll, limit := p.replaceAll, p.limit

	// E. 创建 + ENOENT 合并处理
	info, statErr := os.Stat(resolvedPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			if oldStr == "" {
				// ENOENT + old_string="" → 简单拒绝（审计 C2 简化）
				return nil, fmt.Errorf("%s", BuildGuide(
					fmt.Sprintf("尝试编辑文件 %q，但该文件不存在", resolvedPath),
					"目标文件在文件系统中不存在（ENOENT）",
					"如需创建新文件请使用 Write 工具；若文件确实存在，先用 Glob 或 Ls 核对路径拼写",
				))
			}
			// ENOENT + old_string!="" → CWD 前缀建议
			return nil, fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("尝试编辑文件 %q，但该文件在当前工作区内不存在", resolvedPath),
				"目标文件在文件系统中不存在（ENOENT），可能是路径拼写有误或文件尚未创建",
				"先自查：路径是否拼写正确、文件是否已创建？可用 Glob 或 Ls 工具定位正确的文件路径；若文件尚未创建，改用 Write 工具创建它",
			))
		}
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("访问", resolvedPath, statErr), statErr)
	}

	// 存在但为空 + old_string="" → 创建模式
	if info.Size() == 0 && oldStr == "" {
		dir := filepath.Dir(resolvedPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建父目录", dir, err), err)
		}
		if err := os.WriteFile(resolvedPath, []byte(newStr), 0644); err != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("写入", resolvedPath, err), err)
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
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试编辑文件 %q，但 old_string 为空而文件已有内容", resolvedPath),
			"old_string 为空，但文件非空，无法确定要替换的内容",
			"请明确指定要替换的内容；使用 Write 工具可完全覆盖文件",
		))
	}

	// C. Staleness 自动检测
	if err := filestate.CheckStale(resolvedPath); err != nil {
		return nil, err
	}

	// 读取文件内容
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("读取", resolvedPath, err), err)
	}
	fileContent := string(content)

	// B. 容错匹配
	actualOld, found := findEditMatch(fileContent, oldStr)
	if !found {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试在文件 %q 中查找 old_string %q 以执行替换", resolvedPath, oldStr),
			"文件中不存在该文本，可能是内容、空格或缩进不完全一致",
			"先用 Read 工具确认文件当前内容，确保 old_string 与文件内容完全一致（含空格、缩进）后重试",
		))
	}

	// 检查替换模式
	totalMatches := strings.Count(fileContent, actualOld)
	if !replaceAll && limit <= 0 && totalMatches > 1 {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试编辑文件 %q，但 old_string 在文件中出现了 %d 次", resolvedPath, totalMatches),
			"old_string 在文件中多次出现，单次替换无法确定要替换哪一处",
			"使用 replace_all=true 或 limit=N 替换多次出现",
		))
	}

	if limit < -1 {
		return nil, fmt.Errorf("%s", GuideInvalidValue("Edit", "limit", limit, "limit 只接受 -1、0 或正整数"))
	}

	// 执行替换
	var updatedContent string
	var replaceMode string
	var replaceCount int

	switch {
	case limit == -1:
		updatedContent = strings.ReplaceAll(fileContent, actualOld, newStr)
		replaceCount = strings.Count(updatedContent, newStr) // 近似值
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
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建父目录", dir, err), err)
	}

	// 写入文件
	if err := os.WriteFile(resolvedPath, []byte(updatedContent), 0644); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("写入", resolvedPath, err), err)
	}

	// 写入后重新记录 StaleState（基于刚写入的内容），
	// 使同一文件的连续多次编辑无需反复 Read（与 write.go 模式一致）
	filestate.SetStale(resolvedPath, time.Now(), []byte(updatedContent))
	invalidateNegativeCache(resolvedPath)

	logger.Info("file edited",
		"resolved_path", resolvedPath,
		"old_len", len(oldStr),
		"new_len", len(newStr),
		"replace_mode", replaceMode,
		"replace_count", replaceCount,
	)

	// 生成 unified diff（与 write.go 的截断策略一致），供前端展示 +/- 行数与 diff 视图
	var diffStr string
	if len(fileContent) > 0 && len(fileContent) <= 8*1024 {
		_, diffStr = diffutil.GenerateDiff(fileContent, updatedContent)
	}

	return &EditResult{
		Success:      true,
		FilePath:     resolvedPath,
		Scope:        string(scope),
		ReplaceCount: replaceCount,
		TotalMatches: totalMatches,
		ReplaceMode:  replaceMode,
		Diff:         diffStr,
	}, nil
}

// Execute 编排 Edit 工具执行流程：validate → authorize → perform。
func (t *EditTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	// 1. 参数验证
	p, err := validateEditParams(params)
	if err != nil {
		return nil, err
	}

	// 2. 路径解析 + 安全授权
	resolvedPath, scope, err := t.authorizeEdit(ctx, p.filePath)
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)
	logger.Info("editing file",
		"input_path", p.filePath,
		"resolved_path", resolvedPath,
		"scope", scope,
	)

	// 3. 执行编辑核心逻辑
	return performEdit(logger, resolvedPath, scope, p)
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

package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/events"
)

// Write 实现了文件写入工具。
// 支持将内容写入本地文件系统，具有以下特性：
//   - 覆盖模式：默认行为，覆盖已有文件
//   - 追加模式：通过 append=true 参数启用
//   - 自动创建目录：如果父目录不存在会自动创建
//
// 安全级别：LevelSensitive（敏感），因为会修改文件系统
type Write struct {
	info      *ToolInfo
	whitelist []string
}

// AddWhiteList 添加允许写入的目录前缀。
// 当目标路径匹配任一白名单前缀时，Grant() 会直接放行而无需用户确认。
// 通常在工具初始化后、注册到 ToolRegistry 之前调用。
func (w *Write) AddWhiteList(dirs ...string) *Write {
	w.whitelist = append(w.whitelist, dirs...)
	return w
}

// writeDescription 是 Write 工具的简短描述。
const writeDescription = `将内容写入文件。自动创建父目录。使用 append=true 进行追加而不是覆盖。`

// NewWriteTool 创建一个文件写入工具实例。
//
// 返回：
//   - FuncTool: 配置好的 Write 工具实例
func NewWriteTool() *Write {
	return &Write{
		info: &ToolInfo{
			Name:        "Write",
			Description: writeDescription,
			Prompt: `将文件写入本地文件系统。

用法：
- 如果提供的路径存在文件，此工具将覆盖现有文件。如果是现有文件，你必须先使用 Read 工具读取文件内容。如果你没有先读取文件，此工具将失败。
- 修改现有文件时优先使用 Edit 工具——它只发送差异。仅使用此工具创建新文件或完全重写。
- 仅在用户明确要求时使用表情符号。避免在文件中写入表情符号，除非被要求。`,
			Tags:          []string{"file", "filesystem", "write", "create"},
			SecurityLevel: events.LevelSensitive,
			Parameters: []Parameter{
				{Name: "filePath", Type: "string", Description: "要写入的绝对文件路径。", Required: true},
				{Name: "content", Type: "string", Description: "要写入的文件内容。", Required: true},
				{Name: "append", Type: "boolean", Description: "如果为 true，则追加到现有文件而不是覆盖。", Required: false},
			},
		},
	}
}

// Grant implements tools.PermissionRequired. It pre-resolves the target filePath
// and asks "is this write going to land inside the workspace?". Anything
// outside the project/session boundary is escalated to the user — these
// writes may be legitimate (build output dir, mounted volume) but we don't
// guess.
//
// Hard "no" (sensitive files like .env, .ssh) is enforced in Execute and
// is NOT a Grant concern: there's no user override for it, so asking is
// misleading.
func (w *Write) Grant(ctx context.Context, params map[string]any) (bool, string) {
	filePath, _ := params["filePath"].(string)
	if filePath == "" {
		// Let Execute report the missing parameter cleanly.
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		// No session info available (e.g. a unit-test invocation). Fall
		// through to Execute; boundary checks there will use os.Getwd().
		return true, ""
	}

	resolved, _ := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())
	if resolved == "" {
		return true, ""
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		// Check tool-level whitelist first (configured at initialization).
		for _, dir := range w.whitelist {
			if strings.HasPrefix(resolved, dir) {
				return true, ""
			}
		}
		// Then check session whitelist (user "remember my choice").
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Write {
				if strings.HasPrefix(resolved, allowed) {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf(
			"写入 %q 解析为 %q，这在工作区之外。\n%s",
			filePath, resolved, err.Error(),
		)
	}
	return true, ""
}

// Info 返回 Write 工具的元信息。
func (w *Write) Info() *ToolInfo {
	return w.info
}

// Execute 执行文件写入操作。
//
// 处理流程：
//  1. 验证 filePath 和 content 参数（必须为非空字符串）
//  2. 解析并验证目标路径
//  3. 安全性检查（路径不能超出项目目录）
//  4. 自动创建父目录（如果不存在）
//  5. 确定写入模式（覆盖或追加）
//  6. 写入内容到文件
//  7. 返回写入结果统计
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 "filePath" 和 "content"，可选 "append"
//
// 返回：
//   - map[string]any: 包含 success, filePath, mode, bytes_written 等字段
//   - error: 参数错误、路径验证失败或 I/O 错误
func (w *Write) Execute(ctx context.Context, params map[string]any) (any, error) {
	filePath, err := ValidateRequiredString(params, "filePath")
	if err != nil {
		return nil, err
	}

	content, err := ValidateRequiredString(params, "content")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	// Resolve filePath with optional session: prefix support
	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(filePath, tc.Session.ProjectDir(), tc.Session.SessionDir())

	logger.Info("writing file",
		"input_filePath", filePath,
		"resolved_filePath", resolvedPath,
		"scope", scope,
		"content_len", len(content),
	)

	// Security check: block sensitive system files.
	// Workspace boundary enforcement is handled by FileBoundaryChecker
	// at the permission chain level (executor).
	if err := checkSensitiveFiles(resolvedPath); err != nil {
		return nil, err
	}

	// Ensure the parent directory exists
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建目录失败：%w", err)
	}

	// Check if append mode is enabled
	appendMode := false
	if append, ok := params["append"].(bool); ok {
		appendMode = append
	} else if appendStr, ok := params["append"].(string); ok {
		appendMode = appendStr == "true" || appendStr == "1"
	} else if appendNum, ok := params["append"].(float64); ok {
		appendMode = appendNum != 0
	}

	var file *os.File
	if appendMode {
		// Append mode
		file, err = os.OpenFile(resolvedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("打开文件以追加失败：%w", err)
		}
	} else {
		// Overwrite mode
		file, err = os.Create(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("创建文件失败：%w", err)
		}
	}
	defer file.Close()

	// Write content
	bytesWritten, err := file.WriteString(content)
	if err != nil {
		return nil, fmt.Errorf("写入内容失败：%w", err)
	}

	// Get file info
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("获取文件状态失败：%w", err)
	}

	return map[string]any{
		"success":  true,
		"filePath": resolvedPath,
		"scope":    scope,
		"mode": func() string {
			if appendMode {
				return "append"
			} else {
				return "overwrite"
			}
		}(),
		"bytes_written": bytesWritten,
		"total_size":    info.Size(),
		"message": func() string {
			if appendMode {
				return "内容追加成功"
			} else {
				return "文件写入成功"
			}
		}(),
	}, nil
}

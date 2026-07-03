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
	info *ToolInfo // 工具元信息
}

// writeDescription 是 Write 工具的简短描述。
const writeDescription = `Write content to a file. Creates parent directories automatically. Use append=true to append instead of overwrite.`

// NewWriteTool 创建一个文件写入工具实例。
//
// 返回：
//   - FuncTool: 配置好的 Write 工具实例
func NewWriteTool() FuncTool {
	return &Write{
		info: &ToolInfo{
			Name:        "Write",
			Description: writeDescription,
			Prompt: `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path. If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.
- Prefer the file_edit tool for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`,
			Tags:          []string{"file", "filesystem", "write", "create"},
			SecurityLevel: events.LevelSensitive,
			Parameters: []Parameter{
				{Name: "path", Type: "string", Description: "Absolute file path to write to.", Required: true},
				{Name: "content", Type: "string", Description: "File content to write.", Required: true},
				{Name: "append", Type: "boolean", Description: "If true, append to existing file instead of overwriting.", Required: false},
			},
		},
	}
}

// Grant implements tools.PermissionRequired. It pre-resolves the target path
// and asks "is this write going to land inside the workspace?". Anything
// outside the project/session boundary is escalated to the user — these
// writes may be legitimate (build output dir, mounted volume) but we don't
// guess.
//
// Hard "no" (sensitive files like .env, .ssh) is enforced in Execute and
// is NOT a Grant concern: there's no user override for it, so asking is
// misleading.
func (w *Write) Grant(ctx context.Context, params map[string]any) (bool, string) {
	path, _ := params["path"].(string)
	if path == "" {
		// Let Execute report the missing parameter cleanly.
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		// No session info available (e.g. a unit-test invocation). Fall
		// through to Execute; boundary checks there will use os.Getwd().
		return true, ""
	}

	resolved, _ := ResolveTargetPath(path, tc.Session.ProjectDir(), tc.Session.SessionDir())
	if resolved == "" {
		return true, ""
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		// Check session whitelist before asking the user.
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Write {
				if strings.HasPrefix(resolved, allowed) {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf(
			"Write to %q resolves to %q which is outside the workspace.\n%s",
			path, resolved, err.Error(),
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
//  1. 验证 path 和 content 参数（必须为非空字符串）
//  2. 解析并验证目标路径
//  3. 安全性检查（路径不能超出项目目录）
//  4. 自动创建父目录（如果不存在）
//  5. 确定写入模式（覆盖或追加）
//  6. 写入内容到文件
//  7. 返回写入结果统计
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 必须包含 "path" 和 "content"，可选 "append"
//
// 返回：
//   - map[string]any: 包含 success, path, mode, bytes_written 等字段
//   - error: 参数错误、路径验证失败或 I/O 错误
func (w *Write) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, err := ValidateRequiredString(params, "path")
	if err != nil {
		return nil, err
	}

	content, err := ValidateRequiredString(params, "content")
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	// Resolve path with optional session: prefix support
	tc := GetToolContext(ctx)
	resolvedPath, scope := ResolveTargetPath(path, tc.Session.ProjectDir(), tc.Session.SessionDir())

	logger.Info("writing file",
		"input_path", path,
		"resolved_path", resolvedPath,
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
		return nil, fmt.Errorf("failed to create directory: %w", err)
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
			return nil, fmt.Errorf("failed to open file for appending: %w", err)
		}
	} else {
		// Overwrite mode
		file, err = os.Create(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create file: %w", err)
		}
	}
	defer file.Close()

	// Write content
	bytesWritten, err := file.WriteString(content)
	if err != nil {
		return nil, fmt.Errorf("failed to write content: %w", err)
	}

	// Get file info
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	return map[string]any{
		"success": true,
		"path":    resolvedPath,
		"scope":   scope,
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
				return "Content appended successfully"
			} else {
				return "File written successfully"
			}
		}(),
	}, nil
}

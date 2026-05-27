package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goreact/events"
)

// LS 实现了目录列表工具。
// 用于浏览文件系统目录结构，返回详细的文件元信息。
//
// 特性：
//   - 返回丰富的元信息：名称、类型、大小、修改时间、权限
//   - 支持递归列出子目录（2 层深度）
//   - 可选显示隐藏文件（以 . 开头的文件）
//   - 结果数量限制防止上下文爆炸
//
// 安全级别：LevelSafe（安全），只读操作
type LS struct {
	info *ToolInfo // 工具元信息
}

// NewLsTool 创建一个 Ls 工具实例。
//
// 返回：
//   - FuncTool: 配置好的 Ls 工具实例
func NewLsTool() FuncTool {
	return &LS{
		info: &ToolInfo{
			Name:               "Ls",
			MaxResultSizeChars: 30000,
			Description:        "List directory contents with file metadata — size, type, permissions, modification time. Supports recursive tree view and hidden files.",
			Prompt: `List the contents of a directory to browse the filesystem structure. Use this when you need to see what files exist in a directory, check file sizes, or explore the project layout before reading or editing files.

## Operations

### Basic listing — See files in a directory
Call with no parameters to list the current directory. Each entry includes: name, type (file/directory), size in bytes, modification time, and Unix permissions.

### Recursive tree view
Set recursive=true to show the full directory tree two levels deep. Sub-directories expand with their own children listed under them.

### Show hidden files
Set show_hidden=true to include dot-files (.gitignore, .env, .config, etc.). Hidden files are excluded by default.

## When to use this vs other tools
- Use Ls to explore what's in a directory before reading files.
- Use Glob to search for files matching a pattern across the whole project.
- Use Read to read a specific file's content.
- When exploring an unfamiliar codebase, start with Ls on the root directory to understand the project structure.`,
			Tags:          []string{"file", "filesystem", "list", "directory"},
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{Name: "path", Type: "string", Description: "Directory path to list. Defaults to current directory ('.').", Required: false},
				{Name: "recursive", Type: "boolean", Description: "If true, recursively list sub-directories (2 levels deep). Default: false.", Required: false},
				{Name: "show_hidden", Type: "boolean", Description: "If true, include dot-files and hidden directories. Default: false.", Required: false},
			},
		},
	}
}

// Info 返回 Ls 工具的元信息。
func (l *LS) Info() *ToolInfo {
	return l.info
}

// maxLsItems 是目录列表返回的最大条目数。
// 超过此数量的目录会被截断，防止上下文爆炸。
const maxLsItems = 500

// Execute 执行目录列表操作。
//
// 处理流程：
//  1. 获取 ToolContext（用于路径验证）
//  2. 确定目标目录（默认为当前目录）
//  3. 安全性检查（路径不能超出项目目录）
//  4. 验证路径存在且是目录
//  5. 读取目录内容
//  6. 根据参数过滤和格式化结果
//
// 参数：
//   - ctx: 上下文（包含 ToolContext）
//   - params: 可选 "path", "recursive", "show_hidden"
//
// 返回：
//   - map[string]any: 包含 success, path, total_items, items 等字段
//   - error: 路径不存在或不是目录时返回错误
func (l *LS) Execute(ctx context.Context, params map[string]any) (any, error) {
	// Get ToolContext for directory awareness (Design-time safety)
	tc := GetToolContext(ctx)

	// Get directory path (defaults to current directory)
	dirPath := "."
	if path, ok := params["path"].(string); ok && path != "" {
		dirPath = path
	}

	// Security check
	if err := ValidateFileSafety(dirPath, tc.ProjectDir); err != nil {
		return nil, err
	}

	// Check if path exists
	info, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory does not exist: %s", dirPath)
		}
		return nil, fmt.Errorf("failed to stat directory: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", dirPath)
	}

	// Get parameters
	recursive := false
	if rec, ok := params["recursive"].(bool); ok {
		recursive = rec
	}

	showHidden := false
	if hidden, ok := params["show_hidden"].(bool); ok {
		showHidden = hidden
	}

	// Read directory contents
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	// Build result
	var items []map[string]any

	for _, entry := range entries {
		if len(items) >= maxLsItems {
			break
		}

		// Skip hidden files unless show_hidden is set
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		finfo, _ := entry.Info()
		item := map[string]any{
			"name": entry.Name(),
			"type": func() string {
				if entry.IsDir() {
					return "directory"
				} else {
					return "file"
				}
			}(),
			"size":    finfo.Size(),
			"modTime": finfo.ModTime().Format("2006-01-02 15:04:05"),
			"mode":    finfo.Mode().String(),
		}

		// If recursive mode and entry is a directory, list its children
		if recursive && entry.IsDir() {
			subDir := filepath.Join(dirPath, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err == nil {
				children := make([]map[string]any, 0)
				for _, subEntry := range subEntries {
					if !showHidden && strings.HasPrefix(subEntry.Name(), ".") {
						continue
					}
					subFinfo, _ := subEntry.Info()
					child := map[string]any{
						"name": subEntry.Name(),
						"type": func() string {
							if subEntry.IsDir() {
								return "directory"
							} else {
								return "file"
							}
						}(),
						"size":    subFinfo.Size(),
						"modTime": subFinfo.ModTime().Format("2006-01-02 15:04:05"),
					}
					children = append(children, child)
				}
				item["children"] = children
			}
		}

		items = append(items, item)
	}

	return map[string]any{
		"success":     true,
		"path":        dirPath,
		"total_items": len(items),
		"items":       items,
		"message":     fmt.Sprintf("Listed %d item(s) in '%s'", len(items), dirPath),
	}, nil
}

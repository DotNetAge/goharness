package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/events"
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
			Description:        "列出目录内容及文件元数据。在读取文件之前使用此工具了解项目结构。对于基于模式的文件搜索，优先使用 Glob。",
			Prompt: `列出目录内容以浏览文件系统结构。当你需要查看目录中存在哪些文件、检查文件大小，或在读取或编辑文件之前了解项目结构时，使用此工具。

## 用法

### 基本列表 — 查看目录中的文件
不带参数调用以列出当前目录。每个条目包括：名称、类型（文件/目录）、字节大小、修改时间和 Unix 权限。

### 递归树形视图
设置 recursive=true 以显示两级深度的完整目录树。子目录会展开并列出其自身的子项。

### 显示隐藏文件
设置 show_hidden=true 以包含点文件（.gitignore、.env、.config 等）。默认情况下隐藏文件被排除。

## 何时使用此工具而非其他工具
- 使用 Ls 在读取文件之前探索目录内容。
- 使用 Glob 在整个项目中搜索匹配模式的文件。
- 使用 Read 读取特定文件的内容。
- 探索不熟悉的代码库时，从根目录开始使用 Ls 了解项目结构。`,
			Tags:          []string{"file", "filesystem", "list", "directory"},
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{Name: "path", Type: "string", Description: "要列出的目录路径。默认为当前目录（'.'）。", Required: false},
				{Name: "recursive", Type: "boolean", Description: "如果为 true，递归列出子目录（2 级深度）。默认值：false。", Required: false},
				{Name: "show_hidden", Type: "boolean", Description: "如果为 true，包含点文件和隐藏目录。默认值：false。", Required: false},
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
	if rawPath, found := GetParam(params, "path"); found {
		if path, ok := rawPath.(string); ok && path != "" {
			dirPath = path
		}
	}

	// Security check
	if err := ValidateFileSafety(dirPath, tc.Session.ProjectDir()); err != nil {
		return nil, err
	}

	// Check if path exists
	info, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("目录不存在：%s", dirPath)
		}
		return nil, fmt.Errorf("获取目录状态失败：%w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("路径不是一个目录：%s", dirPath)
	}

	// Get parameters
	recursive := false
	if rawRec, found := GetParam(params, "recursive"); found {
		if rec, ok := rawRec.(bool); ok {
			recursive = rec
		}
	}

	showHidden := false
	if rawHidden, found := GetParam(params, "show_hidden"); found {
		if hidden, ok := rawHidden.(bool); ok {
			showHidden = hidden
		}
	}

	// Read directory contents
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
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
		"message":     fmt.Sprintf("在 '%s' 中列出了 %d 个项目", dirPath, len(items)),
	}, nil
}

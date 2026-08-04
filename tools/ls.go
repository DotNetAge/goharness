package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/sandbox"
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
//
// 授权语义：项目边界检查由 Grant()（PermissionRequired）负责；
// 越界目录触发授权流程，白名单（AddWhiteList）或会话级白名单内放行。
type LS struct {
	info      *ToolInfo // 工具元信息
	whitelist []string  // 允许列出的目录前缀（绕过项目边界检查）
}

// AddWhiteList 添加允许列出的目录前缀。
// 当目标路径匹配任一白名单前缀时，Grant() 与 Execute() 会跳过项目边界检查，
// 允许列出项目目录之外的目录。通常在工具初始化后、注册到 ToolRegistry 之前调用。
func (l *LS) AddWhiteList(dirs ...string) *LS {
	l.whitelist = append(l.whitelist, dirs...)
	return l
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
## 何时使用此工具而非其他工具
- 使用 Ls 在读取文件之前探索目录内容。
- 使用 Glob 在整个项目中搜索匹配模式的文件。
- 使用 Read 读取特定文件的内容。
- 探索不熟悉的目录时，从根目录开始使用 Ls 了解项目结构。`,
			Tags:          []string{"file", "filesystem", "list", "directory"},
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{Name: "path", Type: "string", Description: "要列出的目录路径。默认为当前目录（'.'）。", Required: false},
				{Name: "recursive", Type: "boolean", Description: "如果为 true，递归列出子目录（2 级深度）。默认值：false。", Required: false},
				{Name: "show_hidden", Type: "boolean", Description: "如果为 true，显示包含点文件和隐藏目录。默认值：false。", Required: false},
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

// Grant 实现 tools.PermissionRequired 接口。
//
// 与 Read / Edit / Bash 保持一致的授权语义：目标目录超出项目边界
// （ValidateFileSafety 失败）时，先放行工具白名单（AddWhiteList）与会话级
// 白名单（PermissionAllowSession 记忆）内的路径，其余越界目录触发授权流程。
func (l *LS) Grant(ctx context.Context, params map[string]any) (bool, string) {
	raw, _ := GetParam(params, "path")
	dirPath, _ := raw.(string)
	if dirPath == "" {
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return true, ""
	}

	resolved, _ := ResolveTargetPath(dirPath, tc.Session.ProjectDir(), tc.Session.SessionDir())
	if resolved == "" {
		return true, ""
	}

	// 沙箱启用时，由沙箱统一做文件安全决策
	if sb := tc.Session.Sandbox(); sb != nil {
		dec := sb.CheckFile(resolved, tc.Session.ProjectDir())
		switch dec.Decision {
		case sandbox.DecisionAllow:
			return true, ""
		case sandbox.DecisionDeny:
			return false, dec.Reason
		case sandbox.DecisionAskUser:
			for _, dir := range l.whitelist {
				if pathWithinScope(dir, resolved) {
					return true, ""
				}
			}
			if tc.SessionWhitelist != nil {
				for _, allowed := range tc.SessionWhitelist.Ls {
					if pathWithinScope(allowed, resolved) {
						return true, ""
					}
				}
			}
			return false, dec.Reason
		}
	}

	// 目录不存在或无法访问时跳过授权：
	// ENOTDIR（路径中间段是文件）、EACCES 等场景目录同样不可能正常列出，
	// 授权后 Execute 同样会失败，直接放行让 Execute 报错，避免浪费一轮用户交互。
	if _, statErr := os.Stat(resolved); statErr != nil {
		return true, ""
	}

	if err := ValidateFileSafety(resolved, tc.Session.ProjectDir()); err != nil {
		for _, dir := range l.whitelist {
			if pathWithinScope(dir, resolved) {
				return true, ""
			}
		}
		if tc.SessionWhitelist != nil {
			for _, allowed := range tc.SessionWhitelist.Ls {
				if pathWithinScope(allowed, resolved) {
					return true, ""
				}
			}
		}
		return false, GuideLsOutsideWorkspace(dirPath, resolved, err)
	}
	return true, ""
}

// lsParams 承载 LS 工具解析后的参数。
type lsParams struct {
	path        string
	recursive   bool
	showHidden  bool
}

// validateLSParams 从参数映射提取 LS 工具参数。
// path 默认为 "."，recursive 和 show_hidden 默认为 false。
func validateLSParams(params map[string]any) lsParams {
	dirPath := "."
	if rawPath, found := GetParam(params, "path"); found {
		if path, ok := rawPath.(string); ok && path != "" {
			dirPath = path
		}
	}

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

	return lsParams{path: dirPath, recursive: recursive, showHidden: showHidden}
}

// authorizeLS 解析目录路径并执行沙箱/敏感文件安全检查。
func authorizeLS(ctx context.Context, dirPath string) (resolvedPath string, err error) {
	tc := GetToolContext(ctx)
	var projectDir, sessionDir string
	if tc != nil && tc.Session != nil {
		projectDir = tc.Session.ProjectDir()
		sessionDir = tc.Session.SessionDir()
	}
	resolvedPath, _ = ResolveTargetPath(dirPath, projectDir, sessionDir)

	// 安全校验：敏感文件硬性拦截。
	// 沙箱启用时由 EnforceFile 统一检查（含符号链接解析，防 TOCTOU）。
	if tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			if err := sb.EnforceFile(resolvedPath, projectDir); err != nil {
				return "", err
			}
		} else if err := checkSensitiveFiles(resolvedPath); err != nil {
			return "", err
		}
	} else if err := checkSensitiveFiles(resolvedPath); err != nil {
		return "", err
	}
	return resolvedPath, nil
}

// entryType 返回目录条目的类型字符串。
func entryType(isDir bool) string {
	if isDir {
		return "directory"
	}
	return "file"
}

// performLS 执行目录列表核心逻辑：存在性检查、读取目录、构建条目、递归子目录。
func performLS(resolvedPath string, p lsParams) (map[string]any, error) {
	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s", GuideLsDirNotFound(resolvedPath))
		}
		return nil, fmt.Errorf("%s", GuideLsStatFailed(resolvedPath, err))
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s", GuideLsNotDirectory(resolvedPath))
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("%s", GuideLsReadFailed(resolvedPath, err))
	}

	var items []map[string]any
	for _, entry := range entries {
		if len(items) >= maxLsItems {
			break
		}
		if !p.showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		finfo, _ := entry.Info()
		item := map[string]any{
			"name":    entry.Name(),
			"type":    entryType(entry.IsDir()),
			"size":    finfo.Size(),
			"modTime": finfo.ModTime().Format("2006-01-02 15:04:05"),
			"mode":    finfo.Mode().String(),
		}

		if p.recursive && entry.IsDir() {
			subDir := filepath.Join(resolvedPath, entry.Name())
			subEntries, err := os.ReadDir(subDir)
			if err == nil {
				children := make([]map[string]any, 0)
				for _, subEntry := range subEntries {
					if !p.showHidden && strings.HasPrefix(subEntry.Name(), ".") {
						continue
					}
					subFinfo, _ := subEntry.Info()
					child := map[string]any{
						"name":    subEntry.Name(),
						"type":    entryType(subEntry.IsDir()),
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
		"path":        resolvedPath,
		"total_items": len(items),
		"items":       items,
		"message":     fmt.Sprintf("在 '%s' 中列出了 %d 个项目", resolvedPath, len(items)),
	}, nil
}

// Execute 编排 LS 工具执行流程：validate → authorize → perform。
func (l *LS) Execute(ctx context.Context, params map[string]any) (any, error) {
	p := validateLSParams(params)

	resolvedPath, err := authorizeLS(ctx, p.path)
	if err != nil {
		return nil, err
	}

	return performLS(resolvedPath, p)
}

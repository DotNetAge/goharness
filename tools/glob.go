package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/sandbox"
)

// const globDefaultTimeout = 30 * time.Second

type fileEntry struct {
	path    string
	modTime time.Time
}

type GlobTool struct {
	MaxResults int
}

func NewGlobTool() FuncTool {
	return &GlobTool{
		MaxResults: 200,
	}
}

func (t *GlobTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Glob",
		MaxResultSizeChars: 30000,
		Description:        "查找文件。当你需要通过文件名模式查找文件时使用此工具。",
		Prompt: `快速文件模式匹配工具，适用于任何规模的文件查找任务。
	**用法**
- 支持 glob 模式，如 "**/*.js" 或 "src/**/*.ts"
- 返回匹配的文件路径，按修改时间排序
- 当你进行可能需要多轮 glob 和 grep 的开放式搜索时，使用 SubAgent 工具代替`,
		Tags:          []string{"file", "search", "pattern", "filesystem", "discovery"},
		SecurityLevel: events.LevelSafe,
		Parameters: []Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: "要匹配的文件模式（例如 '**/*.go'）。",
				Required:    true,
			},
			{
				Name:        "path",
				Type:        "string",
				Description: "要搜索的目录。默认为 '.'。",
				Required:    false,
			},
		},
	}
}

func (t *GlobTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	pattern, err := ValidateRequiredString("Glob", params, "pattern")
	if err != nil {
		return nil, err
	}

	searchPath := "."
	if raw, found := GetParam(params, "path"); found {
		if p, ok := raw.(string); ok && p != "" {
			searchPath = p
		}
	}

	// 统一路径解析：绝对路径化 + ~ 展开 + 相对项目目录解析。
	// 修复前 "~/projects" 会被 os.Stat 当作字面量目录，导致"搜索路径错误"。
	tc := GetToolContext(ctx)
	var projectDir, sessionDir string
	if tc != nil && tc.Session != nil {
		projectDir = tc.Session.ProjectDir()
		sessionDir = tc.Session.SessionDir()
	}
	resolvedSearch, _ := ResolveTargetPath(searchPath, projectDir, sessionDir)

	// Security check：防止 "../" 等相对路径上跳越出工作区。
	// 沙箱启用时用 CheckFileAllowOrDeny（Glob 不实现 PermissionRequired，越界直接拒绝不弹窗）；
	// 沙箱未启用时走旧的 ValidateFileSafety。
	if tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			dec := sb.CheckFileAllowOrDeny(resolvedSearch, projectDir)
			if dec.Decision != sandbox.DecisionAllow {
				return nil, fmt.Errorf("%s", dec.Reason)
			}
		} else if err := ValidateFileSafety(resolvedSearch, projectDir); err != nil {
			return nil, err
		}
	} else if err := ValidateFileSafety(resolvedSearch, projectDir); err != nil {
		return nil, err
	}

	info, err := os.Stat(resolvedSearch)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("遍历", resolvedSearch, err), err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试在路径 %q 下查找文件，但它是文件而不是目录", resolvedSearch),
			"搜索路径不是目录，Glob 只能遍历目录",
			"path 参数应指向目录，使用 Ls 确认目录结构后重试",
		))
	}

	matchPattern := normalizeGlobPattern(pattern)

	var entries []fileEntry
	walkErr := filepath.WalkDir(resolvedSearch, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == resolvedSearch {
			return nil
		}

		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		matched, matchErr := filepath.Match(matchPattern, d.Name())
		if matchErr != nil || !matched {
			return nil
		}

		fi, statErr := d.Info()
		if statErr != nil {
			return nil
		}

		entries = append(entries, fileEntry{path: path, modTime: fi.ModTime()})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("匹配文件模式", resolvedSearch, walkErr), walkErr)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.After(entries[j].modTime)
	})

	if t.MaxResults > 0 && len(entries) > t.MaxResults {
		entries = entries[:t.MaxResults]
	}

	files := make([]string, len(entries))
	for i, e := range entries {
		files[i] = e.path
	}

	return map[string]any{
		"success":       true,
		"matches_found": len(files),
		"files":         files,
	}, nil
}

func normalizeGlobPattern(pattern string) string {
	cleaned := strings.TrimSpace(pattern)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")

	for strings.HasPrefix(cleaned, "**/") {
		cleaned = cleaned[3:]
	}
	cleaned = strings.ReplaceAll(cleaned, "/**/", "/")

	if idx := strings.LastIndex(cleaned, "/"); idx >= 0 {
		cleaned = cleaned[idx+1:]
	}

	if cleaned == "" || cleaned == "*" || cleaned == "**" {
		cleaned = "*"
	}

	return cleaned
}

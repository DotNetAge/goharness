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
		Prompt: `快速文件模式匹配工具，适用于任何规模的代码库
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
	pattern, err := ValidateRequiredString(params, "pattern")
	if err != nil {
		return nil, err
	}

	searchPath := "."
	if raw, found := GetParam(params, "path"); found {
		if p, ok := raw.(string); ok && p != "" {
			searchPath = p
		}
	}

	info, err := os.Stat(searchPath)
	if err != nil {
		return nil, fmt.Errorf("搜索路径错误：%w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("搜索路径不是一个目录：%s", searchPath)
	}

	matchPattern := normalizeGlobPattern(pattern)

	var entries []fileEntry
	walkErr := filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == searchPath {
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
		return nil, fmt.Errorf("glob 失败：%w", walkErr)
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

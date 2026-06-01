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

	"github.com/DotNetAge/goreact/events"
)

const globDefaultTimeout = 30 * time.Second

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
		Description:        "Find files",
		Prompt: `- Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- When you are doing an open ended search that may require multiple rounds of globbing and grepping, use the SubAgent tool instead`,
		Tags:          []string{"file", "search", "pattern", "filesystem", "discovery"},
		SecurityLevel: events.LevelSafe,
		Parameters: []Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: "The file pattern to match (e.g., '**/*.go').",
				Required:    true,
			},
			{
				Name:        "path",
				Type:        "string",
				Description: "The directory to search in. Defaults to '.'.",
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
	if p, ok := params["path"].(string); ok && p != "" {
		searchPath = p
	}

	info, err := os.Stat(searchPath)
	if err != nil {
		return nil, fmt.Errorf("search path error: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("search path is not a directory: %s", searchPath)
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
		return nil, fmt.Errorf("glob failed: %w", walkErr)
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

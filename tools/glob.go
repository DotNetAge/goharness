package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/events"
)

const globDefaultTimeout = 30 * time.Second

// GlobTool implements file path discovery using 'find' or 'fd'.
type GlobTool struct {
	MaxResults int
}

// NewGlobTool creates a Glob tool.
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
				Description: "The directory to search in. Defaults to '.'",
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

	// Verify the search path exists and is a directory
	info, err := os.Stat(searchPath)
	if err != nil {
		return nil, fmt.Errorf("search path error: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("search path is not a directory: %s", searchPath)
	}

	// Use 'find' as a portable fallback, or 'fd' if available.
	// Here we use 'find' with some exclusions for simplicity.
	globCtx, globCancel := context.WithTimeout(ctx, globDefaultTimeout)
	defer globCancel()
	cmd := exec.CommandContext(globCtx, "find", searchPath, "-name", pattern, "-not", "-path", "*/.*")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("glob failed: %v", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		return map[string]any{
			"success":       true,
			"matches_found": 0,
			"files":         []string{},
		}, nil
	}

	// Limit the number of results to prevent context explosion
	if t.MaxResults > 0 && len(files) > t.MaxResults {
		files = files[:t.MaxResults]
	}

	return map[string]any{
		"success":       true,
		"matches_found": len(files),
		"files":         files,
	}, nil
}

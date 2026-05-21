package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/DotNetAge/goreact/core"
)

// GrepTool implements a high-performance search using ripgrep (rg).
// It mimics ClaudeCode's GrepTool with output budget management.
type GrepTool struct {
	MaxResults     int
	MaxOutputChars int
}

// NewGrepTool creates a Grep tool.
func NewGrepTool() core.FuncTool {
	return &GrepTool{
		MaxResults:     100,
		MaxOutputChars: 50000,
	}
}

func (t *GrepTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "Grep",
		MaxResultSizeChars: 50000,
		Description: "A powerful search tool built on ripgrep",
		Prompt: `A powerful search tool built on ripgrep

Usage:
- ALWAYS use Grep for search tasks. NEVER invoke grep or rg as a Bash command. The Grep tool has been optimized for correct permissions and access.
- Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
- Filter files with include parameter (e.g., "*.js", "**/*.tsx")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Use delegate tool for open-ended searches requiring multiple rounds
- Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use interface\{\} to find interface{} in Go code)
- Multiline matching: By default patterns match within single lines only. For cross-line patterns like struct \{[\s\S]*?field, use multiline: true`,
		Tags: []string{"file", "search", "content", "regex", "text"},
		Parameters: []core.Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: "The regex pattern to search for.",
				Required:    true,
			},
			{
				Name:        "include",
				Type:        "string",
				Description: "File glob pattern to include (e.g., '*.go').",
				Required:    false,
			},
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	pattern, _ := params["pattern"].(string)
	include, _ := params["include"].(string)

	args := []string{"--column", "--line-number", "--no-heading", "--color", "never", "--smart-case"}
	if include != "" {
		args = append(args, "-g", include)
	}
	args = append(args, pattern, ".")

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If rg returns 1, it means no matches found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return nil, fmt.Errorf("grep failed: %s", string(output))
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) > t.MaxResults {
		output = []byte(strings.Join(lines[:t.MaxResults], "\n"))
	}

	// Second layer defense: limit total output characters
	resultStr := string(output)
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (output truncated at %d chars, showing first %d of %d matches) ...",
				t.MaxOutputChars, t.MaxResults, len(lines))
	}

	return resultStr, nil
}

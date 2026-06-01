package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const grepDefaultTimeout = 30 * time.Second

type GrepTool struct {
	MaxResults     int
	MaxOutputChars int
}

func NewGrepTool() FuncTool {
	return &GrepTool{
		MaxResults:     100,
		MaxOutputChars: 50000,
	}
}

func (t *GrepTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Grep",
		MaxResultSizeChars: 50000,
		Description:        "A powerful search tool built on ripgrep",
		Prompt: `A powerful search tool built on ripgrep

Usage:
- ALWAYS use Grep for search tasks. NEVER invoke grep or rg as a Bash command. The Grep tool has been optimized for correct permissions and access.
- Supports full regex syntax (e.g., "log.*Error", "function\s+\w+")
- Filter files with include parameter (e.g., "*.js", "**/*.tsx")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Use SubAgent tool for open-ended searches requiring multiple rounds
- Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use interface\{\} to find interface{} in Go code)
- Multiline matching: By default patterns match within single lines only. For cross-line patterns like struct \{[\s\S]*?field, use multiline: true`,
		Tags: []string{"file", "search", "content", "regex", "text"},
		Parameters: []Parameter{
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
			{
				Name:        "output_mode",
				Type:        "string",
				Description: "Output format: 'content' (matching lines with context), 'files_with_matches' (file paths only), 'count' (match counts per file). Default: 'content'.",
				Required:    false,
			},
		},
	}
}

func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	pattern, _ := params["pattern"].(string)
	include, _ := params["include"].(string)
	outputMode, _ := params["output_mode"].(string)

	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	if isRgAvailable() {
		return t.executeWithRg(ctx, pattern, include, outputMode)
	}
	return t.executeNative(ctx, pattern, include, outputMode)
}

func isRgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

func (t *GrepTool) executeWithRg(ctx context.Context, pattern, include, outputMode string) (any, error) {
	args := []string{"--no-heading", "--color", "never", "--smart-case"}
	switch outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		args = append(args, "--column", "--line-number")
	}
	if include != "" {
		args = append(args, "-g", include)
	}
	args = append(args, pattern, ".")

	grepCtx, grepCancel := context.WithTimeout(ctx, grepDefaultTimeout)
	defer grepCancel()
	cmd := exec.CommandContext(grepCtx, "rg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return nil, fmt.Errorf("grep failed: %s", string(output))
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) > t.MaxResults {
		output = []byte(strings.Join(lines[:t.MaxResults], "\n"))
	}

	resultStr := string(output)
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (output truncated at %d chars, showing first %d of %d matches) ...",
				t.MaxOutputChars, t.MaxResults, len(lines))
	}

	return resultStr, nil
}

func (t *GrepTool) executeNative(ctx context.Context, pattern, include, outputMode string) (any, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	searchDir := "."
	var results []string
	totalMatchCount := 0

	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
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
		if include != "" {
			matched, matchErr := filepath.Match(include, d.Name())
			if matchErr != nil || !matched {
				return nil
			}
		}

		relPath := path
		if strings.HasPrefix(path, "./") {
			relPath = path
		} else if path != "." {
			relPath = "." + string(filepath.Separator) + path
		}

		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNum := 0
		fileMatchCount := 0

		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				fileMatchCount++
				totalMatchCount++

				switch outputMode {
				case "files_with_matches":
					if fileMatchCount == 1 {
						results = append(results, relPath)
					}
				case "count":
					continue
				default:
					results = append(results, fmt.Sprintf("%s:%d:%s", relPath, lineNum, line))
				}
			}
		}

		if outputMode == "count" && fileMatchCount > 0 {
			results = append(results, fmt.Sprintf("%s:%d", relPath, fileMatchCount))
		}

		return nil
	}

	if err := filepath.WalkDir(searchDir, walkFn); err != nil {
		return nil, fmt.Errorf("grep failed: %w", err)
	}

	if totalMatchCount == 0 {
		return "No matches found.", nil
	}

	if t.MaxResults > 0 && len(results) > t.MaxResults {
		results = results[:t.MaxResults]
	}

	resultStr := strings.Join(results, "\n")
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (output truncated at %d chars, showing first %d of %d matches) ...",
				t.MaxOutputChars, t.MaxResults, totalMatchCount)
	}

	return resultStr, nil
}

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
		Description:        "本地全文搜索",
		Prompt: `本地全文搜索。
- 支持完整的正则表达式语法（例如 "log.*Error"、"function\s+\w+"）。
- 使用 include 参数过滤文件（例如 "*.js"、"**/*.tsx"）。
- 输出模式："content" 显示匹配行，"files_with_matches" 仅显示文件路径（默认），"count" 显示匹配计数。
- 多行匹配：默认仅在单行内匹配。对于跨行模式，使用 multiline: true。
- 模式语法：ripgrep 字面大括号需要转义（如 interface\{\} 匹配 interface{}）。
- 不要使用 Bash 调用 grep/rg，使用本工具。`,
		Tags: []string{"file", "search", "content", "regex", "text"},
		Parameters: []Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: "要搜索的正则表达式模式。",
				Required:    true,
			},
			{
				Name:        "include",
				Type:        "string",
				Description: "要包含的文件 glob 模式（例如 '*.go'）。",
				Required:    false,
			},
			{
				Name:        "output_mode",
				Type:        "string",
				Description: "输出格式：'content'（带上下文的匹配行）、'files_with_matches'（仅文件路径）、'count'（每个文件的匹配计数）。默认值：'content'。",
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
		return nil, fmt.Errorf("pattern 是必需的")
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
			return "未找到匹配项。", nil
		}
		return nil, fmt.Errorf("grep 失败：%s", string(output))
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) > t.MaxResults {
		output = []byte(strings.Join(lines[:t.MaxResults], "\n"))
	}

	resultStr := string(output)
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (输出在 %d 个字符处被截断，显示前 %d 个匹配项，共 %d 个) ...",
				t.MaxOutputChars, t.MaxResults, len(lines))
	}

	return resultStr, nil
}

func (t *GrepTool) executeNative(ctx context.Context, pattern, include, outputMode string) (any, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("无效的正则表达式模式：%w", err)
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
		return nil, fmt.Errorf("grep 失败：%w", err)
	}

	if totalMatchCount == 0 {
		return "未找到匹配项。", nil
	}

	if t.MaxResults > 0 && len(results) > t.MaxResults {
		results = results[:t.MaxResults]
	}

	resultStr := strings.Join(results, "\n")
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (输出在 %d 个字符处被截断，显示前 %d 个匹配项，共 %d 个) ...",
				t.MaxOutputChars, t.MaxResults, totalMatchCount)
	}

	return resultStr, nil
}

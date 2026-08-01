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
	rawPattern, _ := GetParam(params, "pattern")
	pattern, _ := rawPattern.(string)
	rawInclude, _ := GetParam(params, "include")
	include, _ := rawInclude.(string)
	rawOutputMode, _ := GetParam(params, "output_mode")
	outputMode, _ := rawOutputMode.(string)

	if pattern == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("Grep", "pattern"))
	}

	// 搜索根固定为项目目录（Grep 工具面向项目内全文搜索）。
	// 修复前搜索根是进程 CWD 的 "."，与 Agent 所在项目目录无关，导致搜索不到内容。
	searchRoot := "."
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil && tc.Session.ProjectDir() != "" {
		searchRoot = tc.Session.ProjectDir()
	}

	if isRgAvailable() {
		return t.executeWithRg(ctx, pattern, include, outputMode, searchRoot)
	}
	return t.executeNative(ctx, pattern, include, outputMode, searchRoot)
}

func isRgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

func (t *GrepTool) executeWithRg(ctx context.Context, pattern, include, outputMode, searchRoot string) (any, error) {
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
	args = append(args, pattern, searchRoot)

	grepCtx, grepCancel := context.WithTimeout(ctx, grepDefaultTimeout)
	defer grepCancel()
	cmd := exec.CommandContext(grepCtx, "rg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "未找到匹配项。", nil
		}
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试用 ripgrep 在 %q 中搜索模式 %q", searchRoot, pattern),
			WithErrDetail(fmt.Sprintf("rg 执行失败，其输出为：%s", strings.TrimSpace(string(output))), err),
			"先自查：我传入的 pattern（正则表达式）与 include（文件过滤模式）是否符合 Grep 工具的参数定义（参数名称、类型、取值范围）？若确认无误仍失败，应停止无意义的重试，基于已有信息作答或询问用户",
		), err)
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

func (t *GrepTool) executeNative(ctx context.Context, pattern, include, outputMode, searchRoot string) (any, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
				"尝试编译正则表达式时失败",
				WithErrDetail(fmt.Sprintf("正则模式 %q 语法无效", pattern), err),
				"检查正则语法（括号是否配对、特殊字符是否正确转义），用 Grep 工具参数说明中的示例（如 \"log.*Error\"、\"function\\s+\\w+\"）修正后重试",
			), err)
		}
	}

	searchDir := searchRoot
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

		// 以搜索根为基准展示相对路径，保持输出简洁
		relPath := path
		if rel, relErr := filepath.Rel(searchRoot, path); relErr == nil {
			relPath = rel
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
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("遍历", searchRoot, err), err)
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

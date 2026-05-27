package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// grepDefaultTimeout 是 Grep 工具的默认超时时间。
const grepDefaultTimeout = 30 * time.Second

// GrepTool 实现了基于 ripgrep 的高性能文本搜索工具。
// 使用 ripgrep (rg) 作为后端，提供快速、准确的文件内容搜索。
//
// 特性：
//   - 支持完整正则表达式语法
//   - 多种输出模式：内容、文件列表、匹配计数
//   - 文件类型过滤（通过 include 参数）
//   - 结果数量限制和输出字符数限制
//
// 性能优势：
//   - ripgrep 比 grep/fast 更快
//   - 自动尊重 .gitignore 规则
//   - 支持 Unicode 和多行匹配
type GrepTool struct {
	MaxResults     int // 最大返回结果数量（默认 100）
	MaxOutputChars int // 最大输出字符数（默认 50000）
}

// NewGrepTool 创建一个 Grep 工具实例。
//
// 返回：
//   - FuncTool: 配置好的 Grep 工具实例
func NewGrepTool() FuncTool {
	return &GrepTool{
		MaxResults:     100,
		MaxOutputChars: 50000,
	}
}

// Info 返回 Grep 工具的元信息。
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

// Execute 执行文本搜索操作。
//
// 处理流程：
//  1. 提取搜索参数（pattern, include, output_mode）
//  2. 构建 ripgrep 命令行参数
//  3. 执行 ripgrep 搜索
//  4. 处理搜索结果（限制数量和字符数）
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "pattern"，可选 "include" 和 "output_mode"
//
// 返回：
//   - string: 格式化的搜索结果
//   - error: 搜索执行失败时返回错误
func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	pattern, _ := params["pattern"].(string)
	include, _ := params["include"].(string)
	outputMode, _ := params["output_mode"].(string)

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

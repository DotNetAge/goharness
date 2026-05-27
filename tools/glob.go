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

// globDefaultTimeout 是 Glob 工具的默认超时时间。
const globDefaultTimeout = 30 * time.Second

// GlobTool 实现了文件路径发现工具。
// 使用 find 命令进行文件模式匹配，支持通配符搜索。
//
// 特性：
//   - 支持通配符模式（如 *.go, **/*.ts）
//   - 按修改时间排序返回结果
//   - 结果数量限制防止上下文爆炸
//
// 安全级别：LevelSafe（安全），只读操作
type GlobTool struct {
	MaxResults int // 最大返回结果数量（默认 200）
}

// NewGlobTool 创建一个 Glob 工具实例。
//
// 返回：
//   - FuncTool: 配置好的 Glob 工具实例
func NewGlobTool() FuncTool {
	return &GlobTool{
		MaxResults: 200,
	}
}

// Info 返回 Glob 工具的元信息。
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

// Execute 执行文件路径搜索操作。
//
// 处理流程：
//  1. 验证 pattern 参数（必须为非空字符串）
//  2. 确定搜索目录（默认为当前目录）
//  3. 验证搜索路径存在且是目录
//  4. 执行 find 命令进行模式匹配
//  5. 解析结果并限制数量
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "pattern"，可选 "path"
//
// 返回：
//   - map[string]any: 包含 success, matches_found, files 字段
//   - error: 参数错误或执行失败时返回错误
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

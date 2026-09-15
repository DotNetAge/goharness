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
		// Prompt 仅写参数定义表达不了的行为约定（排序规则、性能定位、路由建议）；
		// 参数语义一律写在 Parameters[].Description，不在 Prompt 重复。
		Prompt:        `按修改时间降序返回匹配文件。遍历自动跳过隐藏目录与 node_modules/dist 等重目录，比 find 更快、结果更聚焦。开放式的多轮 glob+grep 搜索请改用 SubAgent 工具。`,
		Tags:          []string{"file", "search", "pattern", "filesystem", "discovery"},
		SecurityLevel: events.LevelSafe,
		Parameters: []Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: `文件匹配模式。不含路径分隔符（如 '*.go'）时按文件名在任意深度匹配；含路径分隔符（如 'src/**/*.go'、'config/*.yaml'）时按相对 path 的路径分层匹配，'**' 匹配零或多层目录。`,
				Required:    true,
			},
			{
				Name:        "path",
				Type:        "string",
				Description: "要搜索的目录。默认为项目目录。",
				Required:    false,
			},
			{
				Name:        "head_limit",
				Type:        "integer",
				Description: "返回的最大结果数（默认 200，按修改时间降序截断）。",
				Required:    false,
			},
		},
	}
}

// globParams 承载 Glob 工具解析后的参数。
type globParams struct {
	pattern   string
	path      string
	headLimit int
}

// validateGlobParams 从参数映射提取 Glob 工具参数。
// pattern 为必填，path 默认为 "."，head_limit 可选。
func validateGlobParams(params map[string]any) (globParams, error) {
	pattern, err := ValidateRequiredString("Glob", params, "pattern")
	if err != nil {
		return globParams{}, err
	}
	searchPath := "."
	if raw, found := GetParam(params, "path"); found {
		if p, ok := raw.(string); ok && p != "" {
			searchPath = p
		}
	}
	return globParams{pattern: pattern, path: searchPath, headLimit: parseIntParam(params, "head_limit")}, nil
}

// authorizeGlob 解析搜索路径并执行沙箱强制安全检查。
// Glob 不实现 PermissionRequired，越界直接拒绝不弹窗。
func authorizeGlob(ctx context.Context, searchPath string) (resolvedPath string, err error) {
	tc := GetToolContext(ctx)
	var projectDir, sessionDir string
	if tc != nil && tc.Session != nil {
		projectDir = tc.Session.ProjectDir()
		sessionDir = tc.Session.SessionDir()
	}
	resolvedPath, _ = ResolveTargetPath(searchPath, projectDir, sessionDir)

	// 安全校验：工作区边界、敏感文件等策略统一由沙箱强制检查；
	// 越界直接拒绝（Glob 无授权流程，AskUser 视为拒绝）。未注入沙箱时拒绝执行。
	sb, err := requireSandbox(ctx, "Glob")
	if err != nil {
		return "", err
	}
	dec := sb.CheckFileAllowOrDeny(resolvedPath, projectDir)
	if dec.Decision != sandbox.DecisionAllow {
		return "", fmt.Errorf("%s", dec.Reason)
	}
	return resolvedPath, nil
}

// performGlob 执行文件模式匹配核心逻辑：存在性检查、遍历目录、模式匹配、排序、构建结果。
func performGlob(resolvedPath string, p globParams, maxResults int) (any, error) {
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("遍历", resolvedPath, err), err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试在路径 %q 下查找文件，但它是文件而不是目录", resolvedPath),
			"搜索路径不是目录，Glob 只能遍历目录",
			"path 参数应指向目录，使用 Ls 确认目录结构后重试",
		))
	}

	// 模式编译一次（分段 + 折叠连续 **），遍历中零重复解析
	pat := compileGlobPattern(p.pattern)
	// 多段模式（含路径分隔符，含反斜杠写法归一化后的模式）按相对路径段匹配；
	// 先算好相对化前缀避免逐文件 filepath.Rel。
	// 用 len(pat) > 1 判定而非检查原始串是否含 '/'：compileGlobPattern 已把 '\\' 归一化为
	// '/'，原始串判定会让 "src\*.go" 静默退化为 basename 任意深度匹配（作用域丢失）。
	relPrefix := ""
	if len(pat) > 1 {
		relPrefix = resolvedPath + string(filepath.Separator)
	}

	// segBuf 是遍历热路径的栈上分段缓冲（闭包内声明不逃逸，跨文件复用零分配）
	var segBuf [globStackSegs]string

	var entries []fileEntry
	walkErr := filepath.WalkDir(resolvedPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == resolvedPath {
			return nil
		}

		if d.IsDir() {
			// 跳过隐藏目录与默认重目录集合（性能兜底：比 find 快且结果不被依赖目录淹没）
			name := d.Name()
			if strings.HasPrefix(name, ".") || defaultSkipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		matched := matchGlobEntry(pat, relPrefix, path, d.Name(), segBuf[:])
		if !matched {
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
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("匹配文件模式", resolvedPath, walkErr), walkErr)
	}

	// 修改时间降序（最新优先，便于定位近期变更的文件）
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.After(entries[j].modTime)
	})

	// head_limit > 0 时以其为上限，否则回落到 MaxResults（默认 200，防超长输出）
	limit := maxResults
	if p.headLimit > 0 {
		limit = p.headLimit
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	files := make([]string, len(entries))
	for i, e := range entries {
		files[i] = e.path
	}

	return MetaMap{
		Data: map[string]any{
			"success":       true,
			"matches_found": len(files),
			"files":         files,
		},
		Meta: map[string]any{
			"match_count": len(files),
		},
	}, nil
}

// Execute 编排 Glob 工具执行流程：validate → authorize → perform。
func (t *GlobTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	p, err := validateGlobParams(params)
	if err != nil {
		return nil, err
	}

	resolvedPath, err := authorizeGlob(ctx, p.path)
	if err != nil {
		return nil, err
	}

	return performGlob(resolvedPath, p, t.MaxResults)
}

// globSegPattern 为编译后的 glob 模式（按 "/" 分段）。
type globSegPattern []string

// compileGlobPattern 编译模式：统一分隔符、去空段、折叠连续 "**"（避免匹配器指数回溯）。
func compileGlobPattern(pattern string) globSegPattern {
	cleaned := strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/")
	cleaned = strings.TrimPrefix(cleaned, "./")
	parts := strings.Split(cleaned, "/")
	segs := make([]string, 0, len(parts))
	for _, s := range parts {
		if s == "" || s == "." {
			continue
		}
		if s == "**" && len(segs) > 0 && segs[len(segs)-1] == "**" {
			continue
		}
		segs = append(segs, s)
	}
	return segs
}

// globStackSegs 是遍历热路径的栈上分段缓冲容量（超深路径罕见，超出回退堆分配）。
const globStackSegs = 32

// splitPathSegs 把相对路径切分为路径段，优先写入 buf（段切片共享底层字符串，零拷贝），
// 段数超出 buf 容量时回退 strings.Split（堆分配）。
func splitPathSegs(rel string, buf []string) []string {
	if n := strings.Count(rel, "/") + 1; n <= len(buf) {
		segs := buf[:n]
		i := 0
		for len(rel) > 0 {
			if idx := strings.IndexByte(rel, '/'); idx >= 0 {
				segs[i] = rel[:idx]
				rel = rel[idx+1:]
			} else {
				segs[i] = rel
				rel = ""
			}
			i++
		}
		return segs
	}
	return strings.Split(rel, "/")
}

// matchGlobEntry 判断单个文件是否命中模式：
//   - 模式单段（如 '*.go'、'**'）：按文件名在任意深度匹配（fd 风格，Agent 最高频用法）
//   - 模式多段（如 'src/**/*.go'）：按相对 searchDir 的路径分层匹配，"**" 段匹配零或多层
//     目录，且路径必须被完整消费（'src/*.go' 不命中 'src/sub/a.go'，与 shell globstar 一致）
func matchGlobEntry(pat globSegPattern, relPrefix, path, baseName string, segBuf []string) bool {
	if relPrefix == "" {
		matched, err := filepath.Match(pat.lastOrSelf(), baseName)
		return err == nil && matched
	}
	rel := strings.TrimPrefix(path, relPrefix)
	return matchGlobSegments(pat, splitPathSegs(rel, segBuf))
}

// lastOrSelf 返回末段（裸 basename 模式经编译后即文件名模式）；空模式返回 "*" 兜底。
func (g globSegPattern) lastOrSelf() string {
	if len(g) == 0 {
		return "*"
	}
	return g[len(g)-1]
}

// matchGlobSegments 判断相对路径段序列是否匹配模式段序列（globstar 语义）。
// "**" 匹配零或多层目录；其余段用 filepath.Match 逐段匹配。
func matchGlobSegments(pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			// "**" 展开为零或多层：穷举切割点
			for i := 0; i <= len(path); i++ {
				if matchGlobSegments(rest, path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		matched, err := filepath.Match(pat[0], path[0])
		if err != nil || !matched {
			return false
		}
		pat, path = pat[1:], path[1:]
	}
	// 模式已消费完：路径必须同样消费完（前缀匹配不成立）
	return len(path) == 0
}

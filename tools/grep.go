package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DotNetAge/goharness/sandbox"
)

const grepDefaultTimeout = 30 * time.Second

// native 遍历与 Glob 遍历时默认跳过的重目录（rg 路径由 .gitignore 机制天然覆盖，
// 内置遍历无此机制，用固定集合兜底，避免 node_modules 这类目录拖垮搜索/淹没结果）。
var defaultSkipDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
}

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
		Prompt:             `本地全文搜索`,
		Tags:               []string{"file", "search", "content", "regex", "text"},
		Parameters: []Parameter{
			{
				Name:        "pattern",
				Type:        "string",
				Description: `要搜索的正则表达式模式（支持完整正则语法，如 "log.*Error"、"function\s+\w+"）。`,
				Required:    true,
			},
			{
				Name:        "path",
				Type:        "string",
				Description: "搜索目标：项目内相对子目录（或项目内绝对路径）。默认为项目根目录；自动跳过 .git/node_modules/dist 等目录与隐藏目录。",
				Required:    false,
			},
			{
				Name:        "include",
				Type:        "string",
				Description: "只搜索文件名匹配该 glob 的文件（例如 '*.go'）。",
				Required:    false,
			},
			{
				Name:        "exclude",
				Type:        "string",
				Description: "排除文件名或目录名匹配该 glob 的文件/目录（例如 '*.min.js' 或 'testdata'）。",
				Required:    false,
			},
			{
				Name:        "output_mode",
				Type:        "string",
				Description: "输出格式：'content'（匹配行，可带上下文）、'files_with_matches'（仅文件路径）、'count'（每个文件的匹配计数）。默认值：'content'。",
				Required:    false,
			},
			{
				Name:        "ignore_case",
				Type:        "boolean",
				Description: "true 时强制忽略大小写。默认 false（smart-case：模式全小写才忽略大小写）。",
				Required:    false,
			},
			{
				Name:        "context",
				Type:        "integer",
				Description: "每个匹配行前后各附带 N 行上下文（仅 content 模式生效）。",
				Required:    false,
			},
			{
				Name:        "context_before",
				Type:        "integer",
				Description: "每个匹配行前方附带 N 行上下文（优先于 context，仅 content 模式生效）。",
				Required:    false,
			},
			{
				Name:        "context_after",
				Type:        "integer",
				Description: "每个匹配行后方附带 N 行上下文（优先于 context，仅 content 模式生效）。",
				Required:    false,
			},
			{
				Name:        "head_limit",
				Type:        "integer",
				Description: "返回的最大行数（默认 100）。",
				Required:    false,
			},
			{
				Name:        "offset",
				Type:        "integer",
				Description: "跳过前 N 行结果（配合 head_limit 翻页）。",
				Required:    false,
			},
		},
	}
}

// grepParams 承载解析后的 Grep 参数，两个引擎（rg/native）共用同一份语义。
type grepParams struct {
	pattern       string
	path          string
	include       string
	exclude       string
	outputMode    string
	ignoreCase    bool // true=强制忽略大小写；false=smart-case（模式含大写才区分）
	contextBefore int
	contextAfter  int
	headLimit     int
	offset        int
}

// GetParam 按键取值并做类型断言的薄封装，缺失或类型不符一律取零值（宽松解析，
// 与 Bash 工具的 working_dir 等可选参数处理一致；pattern 缺失在 Execute 显式报错）。
func grepString(params map[string]any, key string) string {
	if raw, ok := GetParam(params, key); ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

// parseIntParam 从参数映射提取正整数（JSON 数字反序列化为 float64），
// 缺失、类型不符或非正值一律返回 0（宽松解析，由调用方决定默认值语义）。
// Grep/Glob 的可选数值参数共用此 helper。
func parseIntParam(params map[string]any, key string) int {
	if raw, ok := GetParam(params, key); ok {
		if f, ok := raw.(float64); ok && f > 0 {
			return int(f)
		}
	}
	return 0
}

// validateGrepParams 提取并归一化参数：上下文仅 content 模式生效；context=N 拆分为 before/after。
func validateGrepParams(params map[string]any) grepParams {
	p := grepParams{
		pattern:    grepString(params, "pattern"),
		path:       grepString(params, "path"),
		include:    grepString(params, "include"),
		exclude:    grepString(params, "exclude"),
		outputMode: grepString(params, "output_mode"),
		ignoreCase: false,
		headLimit:  parseIntParam(params, "head_limit"),
		offset:     parseIntParam(params, "offset"),
	}
	if raw, ok := GetParam(params, "ignore_case"); ok {
		if b, ok := raw.(bool); ok {
			p.ignoreCase = b
		}
	}

	// 上下文仅在 content（含默认空值）模式有意义
	if p.outputMode == "" || p.outputMode == "content" {
		p.contextBefore = parseIntParam(params, "context_before")
		p.contextAfter = parseIntParam(params, "context_after")
		if p.contextBefore == 0 && p.contextAfter == 0 {
			if n := parseIntParam(params, "context"); n > 0 {
				p.contextBefore, p.contextAfter = n, n
			}
		}
	}
	return p
}

// isIgnoreCaseMatch 实现 smart-case 语义：模式中不含大写字母即忽略大小写（与 rg 一致）。
func isIgnoreCaseMatch(p grepParams) bool {
	if p.ignoreCase {
		return true
	}
	for _, r := range p.pattern {
		if unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// resolveSearchTarget 解析 path 参数为实际搜索目录，并强制其位于项目目录边界内。
// 返回绝对路径；path 为空时返回项目目录本身。
func (t *GrepTool) resolveSearchTarget(pathParam, projectDir string) (string, error) {
	base, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	if pathParam == "" {
		return base, nil
	}

	var target string
	if filepath.IsAbs(pathParam) {
		// 绝对路径：仅允许项目目录内的路径（Join 会错误吞掉绝对路径，需单独处理）
		target = filepath.Clean(pathParam)
	} else {
		target, err = filepath.Abs(filepath.Join(projectDir, pathParam))
		if err != nil {
			return "", err
		}
	}

	// 边界校验：目标必须等于 base 或位于 base 之下（防 "../../" 逃逸）
	if target != base && !strings.HasPrefix(target, base+string(filepath.Separator)) {
		return "", fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("解析搜索路径 %q", pathParam),
			fmt.Sprintf("搜索路径 %q 越出项目目录 %q 边界", pathParam, projectDir),
			"Grep 的 path 参数仅支持项目内子目录；跨项目搜索请改用 Bash 工具",
		))
	}
	return target, nil
}

// buildRgArgs 组装 rg 命令行参数（纯函数，便于测试校验参数映射）。
func buildRgArgs(p grepParams, searchDir string) []string {
	args := []string{"--no-heading", "--color", "never"}
	// 大小写：smart-case 为默认；ignore_case=true 时改用 -i
	if p.ignoreCase {
		args = append(args, "-i")
	} else {
		args = append(args, "--smart-case")
	}
	switch p.outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		args = append(args, "--column", "--line-number")
		// 上下文：before/after 相等时合并为 -C
		switch {
		case p.contextBefore > 0 && p.contextBefore == p.contextAfter:
			args = append(args, "-C", fmt.Sprint(p.contextBefore))
		case p.contextBefore > 0:
			args = append(args, "-B", fmt.Sprint(p.contextBefore))
		case p.contextAfter > 0:
			args = append(args, "-A", fmt.Sprint(p.contextAfter))
		}
	}
	if p.include != "" {
		args = append(args, "-g", p.include)
	}
	if p.exclude != "" {
		args = append(args, "-g", "!"+p.exclude)
	}
	return append(args, p.pattern, searchDir)
}

// pageLines 对输出行做 offset 跳过与 limit 截断（两引擎共用的统一分页层）。
// head_limit > 0 时以其为上限，否则回落到 MaxResults（默认 100，防超长输出）。
func (t *GrepTool) pageLines(lines []string, p grepParams) []string {
	if p.offset > 0 {
		if p.offset >= len(lines) {
			return nil
		}
		lines = lines[p.offset:]
	}
	limit := t.MaxResults
	if p.headLimit > 0 {
		limit = p.headLimit
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

func (t *GrepTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	p := validateGrepParams(params)
	if p.pattern == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("Grep", "pattern"))
	}

	// 安全校验：命令白名单、工作区边界等策略统一由沙箱强制检查。
	// 未注入沙箱时拒绝执行（安全决策统一收口到沙箱，工具自身不做授权检查）。
	if _, err := requireSandbox(ctx, "Grep"); err != nil {
		return nil, err
	}

	// 搜索根默认项目目录；path 参数可收窄到项目内子目录（边界强制校验）。
	// 修复前搜索根是进程 CWD 的 "."，与 Agent 所在项目目录无关，导致搜索不到内容。
	searchRoot := "."
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil && tc.Session.ProjectDir() != "" {
		searchRoot = tc.Session.ProjectDir()
	}
	searchDir, err := t.resolveSearchTarget(p.path, searchRoot)
	if err != nil {
		return nil, err
	}

	if isRgAvailable() {
		return t.runRg(ctx, p, searchDir)
	}
	return t.runNative(ctx, p, searchDir)
}

// rg 可用性探测缓存：Grep 是极高频工具，exec.LookPath 每次都会遍历 PATH 全部目录做 stat，
// 用 sync.Once 进程内只探测一次（进程生命周期内 rg 的安装状态视为不变）。
var rgAvailability = struct {
	sync.Once
	ok bool
}{}

func isRgAvailable() bool {
	rgAvailability.Do(func() {
		_, err := exec.LookPath("rg")
		rgAvailability.ok = err == nil
	})
	return rgAvailability.ok
}

func (t *GrepTool) runRg(ctx context.Context, p grepParams, searchDir string) (any, error) {
	args := buildRgArgs(p, searchDir)

	grepCtx, grepCancel := context.WithTimeout(ctx, grepDefaultTimeout)
	defer grepCancel()

	// 沙箱启用时，用 CheckCommand 做强制检查（防 rg 被策略禁用或命令被注入危险模式）。
	// rg 在默认白名单内，正常情况下 CheckCommand 返回 Allow；策略禁用时返回 Deny 或 AskUser。
	// Grep 没有 Grant 方法（不弹窗），Execute 阶段 AskUser 视为拒绝（与 Bash enforceWithSandbox 语义一致）。
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			dec := sb.CheckCommand("rg " + strings.Join(args, " "))
			if dec.Decision == sandbox.DecisionDeny || dec.Decision == sandbox.DecisionAskUser {
				return nil, fmt.Errorf("%s", dec.Reason)
			}
		}
	}

	// 流式读取 stdout：输出一旦达到 MaxOutputChars 上限立即 kill rg（提前结束仓库遍历，
	// 内存有硬上界）。修复前 CombinedOutput 会缓冲全量输出——宽泛 pattern（如 '^'）在大仓库
	// 上要扫完整棵树才裁剪，最坏 30s 超时 + 百 MB 级内存缓冲。
	// stderr 单独收集：权限警告等消息不再混入 stdout 被误计为结果行。
	cmd := exec.CommandContext(grepCtx, "rg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 rg 标准输出管道失败：%w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试用 ripgrep 在 %q 中搜索模式 %q", searchDir, p.pattern),
			WithErrDetail("rg 启动失败", err),
			"先自查：rg 是否已安装可用？若确认无误仍失败，应停止无意义的重试，基于已有信息作答或询问用户",
		), err)
	}

	var out bytes.Buffer
	killed := false
	chunk := make([]byte, 64*1024)
	for {
		n, readErr := stdout.Read(chunk)
		// killed 后跳过写入：kill 与进程退出之间存在窗口期，残余输出不再累积，
		// 保证 out 严格不超过上限（残行丢弃逻辑只依赖 kill 前的数据，不受影响）
		if n > 0 && !killed {
			out.Write(chunk[:n])
			if t.MaxOutputChars > 0 && out.Len() >= t.MaxOutputChars {
				killed = true
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	waitErr := cmd.Wait()
	if !killed && waitErr != nil {
		// 退出码 1 是 rg 的"无匹配"约定，不是错误
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return MetaString{Value: "未找到匹配项。", Meta: map[string]any{"hit_count": 0}}, nil
		}
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试用 ripgrep 在 %q 中搜索模式 %q", searchDir, p.pattern),
			WithErrDetail(fmt.Sprintf("rg 执行失败，其输出为：%s", strings.TrimSpace(stderr.String())), waitErr),
			"先自查：我传入的 pattern（正则表达式）与 include（文件过滤模式）是否符合 Grep 工具的参数定义（参数名称、类型、取值范围）？若确认无误仍失败，应停止无意义的重试，基于已有信息作答或询问用户",
		), waitErr)
	}

	// 按行分页：hit_count 记录分页前的总行数，offset/head_limit 由 pageLines 统一处理
	outStr := strings.TrimRight(out.String(), "\n")
	if killed {
		// 提前终止：输出可能停在残行中间（甚至多字节字符中间），丢弃最后一个不完整行
		if idx := strings.LastIndexByte(outStr, '\n'); idx >= 0 {
			outStr = outStr[:idx]
		}
	}
	var lines []string
	if outStr != "" {
		lines = strings.Split(outStr, "\n")
	}
	total := len(lines)
	shown := t.pageLines(lines, p)

	resultStr := strings.Join(shown, "\n")
	// 字符上界兜底（覆盖提前终止时残字符等场景；UTF-8 下字符数 ≤ 字节数）
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		resultStr = string([]rune(resultStr)[:t.MaxOutputChars])
	}
	if killed {
		resultStr += fmt.Sprintf("\n... (输出超过 %d 字符上限，已提前终止 rg 搜索，统计仅为已产出结果) ...", t.MaxOutputChars)
	}

	return MetaString{Value: resultStr, Meta: map[string]any{"hit_count": total}}, nil
}

func (t *GrepTool) runNative(ctx context.Context, p grepParams, searchDir string) (any, error) {
	// 与 rg 的 smart-case 语义对齐：模式不含大写字母时强制忽略大小写（统一两引擎行为），
	// 修复前 native 路径无条件 "(?i)"，与 rg 路径同一 pattern 结果不同
	prefix := ""
	if isIgnoreCaseMatch(p) {
		prefix = "(?i)"
	}
	re, err := regexp.Compile(prefix + p.pattern)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试编译正则表达式时失败",
			WithErrDetail(fmt.Sprintf("正则模式 %q 语法无效", p.pattern), err),
			"检查正则语法（括号是否配对、特殊字符是否正确转义），用 Grep 工具参数说明中的示例（如 \"log.*Error\"、\"function\\s+\\w+\"）修正后重试",
		), err)
	}

	var results []string
	totalMatchCount := 0

	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			// 跳过隐藏目录、exclude 命中的目录与默认重目录集合
			name := d.Name()
			if strings.HasPrefix(name, ".") || defaultSkipDirs[name] {
				return filepath.SkipDir
			}
			if p.exclude != "" {
				if matched, matchErr := filepath.Match(p.exclude, name); matchErr == nil && matched {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if p.include != "" {
			matched, matchErr := filepath.Match(p.include, d.Name())
			if matchErr != nil || !matched {
				return nil
			}
		}
		if p.exclude != "" {
			if matched, matchErr := filepath.Match(p.exclude, d.Name()); matchErr == nil && matched {
				return nil
			}
		}

		// 以搜索根为基准展示相对路径，保持输出简洁
		relPath := path
		if rel, relErr := filepath.Rel(searchDir, path); relErr == nil {
			relPath = rel
		}

		// 沙箱启用时，用 EnforceFile 检查文件（防符号链接越界，如 .hidden → /etc/passwd）。
		// 搜索根固定为 ProjectDir，正常文件均在边界内；符号链接越界时 EnforceFile 拒绝，跳过此文件。
		if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
			if sb := tc.Session.Sandbox(); sb != nil {
				if err := sb.EnforceFile(path, searchDir); err != nil {
					return nil
				}
			}
		}

		withContext := p.contextBefore > 0 || p.contextAfter > 0
		var fileResults []string
		var fileMatchCount int

		if withContext {
			// 带上下文：两遍法——先收集命中行号并合并区间，再重读文件按区间输出。
			// 匹配行用 `path:line:text`、上下文行用 `path-line:text`（grep 惯例），
			// 组间以 `--` 分隔，与 rg 的输出格式一致。
			matchLines, err := grepCollectMatches(path, re)
			if err != nil || len(matchLines) == 0 {
				return nil
			}
			fileMatchCount = len(matchLines)
			matchSet := make(map[int]bool, len(matchLines))
			for _, ln := range matchLines {
				matchSet[ln] = true
			}
			intervals := mergeIntervals(matchLines, p.contextBefore, p.contextAfter)
			fileResults, err = grepEmitWithContext(path, relPath, intervals, matchSet)
			if err != nil {
				return nil
			}
		} else {
			fileResults, fileMatchCount, _ = grepScanFile(path, relPath, re, p.outputMode)
		}

		if len(fileResults) == 0 {
			return nil
		}
		totalMatchCount += fileMatchCount
		results = append(results, fileResults...)
		return nil
	}

	if err := filepath.WalkDir(searchDir, walkFn); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("遍历", searchDir, err), err)
	}

	if totalMatchCount == 0 {
		return MetaString{Value: "未找到匹配项。", Meta: map[string]any{"hit_count": 0}}, nil
	}

	// 与 rg 路径一致的分页语义：hit_count 为分页前总输出行数
	total := len(results)
	shown := t.pageLines(results, p)

	resultStr := strings.Join(shown, "\n")
	if t.MaxOutputChars > 0 && len(resultStr) > t.MaxOutputChars {
		runes := []rune(resultStr)
		resultStr = string(runes[:t.MaxOutputChars]) +
			fmt.Sprintf("\n... (输出在 %d 个字符处被截断，显示前 %d 个匹配项，共 %d 个) ...",
				t.MaxOutputChars, len(shown), total)
	}

	return MetaString{Value: resultStr, Meta: map[string]any{"hit_count": total}}, nil
}

// grepScanFile 无上下文扫描：按 output_mode 输出（content 行 / 文件路径 / 计数）。
func grepScanFile(path, relPath string, re *regexp.Regexp, outputMode string) (results []string, matchCount int, err error) {
	file, openErr := os.Open(path)
	if openErr != nil {
		return nil, 0, openErr
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if !re.MatchString(line) {
			continue
		}
		matchCount++
		switch outputMode {
		case "files_with_matches":
			// 首个命中即记录路径并提前退出（无需继续扫描）
			if matchCount == 1 {
				results = append(results, relPath)
			}
		case "count":
			// 仅计数，不输出行
		default:
			results = append(results, fmt.Sprintf("%s:%d:%s", relPath, lineNum, line))
		}
	}
	if outputMode == "count" && matchCount > 0 {
		results = append(results, fmt.Sprintf("%s:%d", relPath, matchCount))
	}
	return results, matchCount, scanner.Err()
}

// grepCollectMatches 第一遍扫描：仅收集命中行号（升序）。
func grepCollectMatches(path string, re *regexp.Regexp) ([]int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var matchLines []int
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if re.MatchString(scanner.Text()) {
			matchLines = append(matchLines, lineNum)
		}
	}
	return matchLines, scanner.Err()
}

// mergeIntervals 将命中行号扩展为 [m-before, m+after] 区间并合并重叠部分。
func mergeIntervals(matchLines []int, before, after int) [][2]int {
	var intervals [][2]int
	for _, m := range matchLines {
		lo, hi := m-before, m+after
		if lo < 1 {
			lo = 1
		}
		if n := len(intervals); n > 0 && lo <= intervals[n-1][1]+1 {
			// 与上一区间相邻或重叠则合并（相邻也合并，避免输出碎片化）
			if hi > intervals[n-1][1] {
				intervals[n-1][1] = hi
			}
			continue
		}
		intervals = append(intervals, [2]int{lo, hi})
	}
	return intervals
}

// grepEmitWithContext 第二遍扫描：按区间输出匹配行与上下文行。
// 匹配行用 ":" 分隔、上下文行用 "-" 分隔，组间插入 "--" 分隔行。
func grepEmitWithContext(path, relPath string, intervals [][2]int, matchSet map[int]bool) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var results []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	intervalIdx := 0
	emitting := false // 当前是否处于某个输出区间内（用于组间插入 "--" 分隔）

	for scanner.Scan() {
		lineNum++
		// 推进到 lineNum 所处的区间（越过的区间关闭 emitting，组间补 "--"）
		for intervalIdx < len(intervals) && lineNum > intervals[intervalIdx][1] {
			intervalIdx++
			emitting = false
		}
		if intervalIdx >= len(intervals) {
			break
		}
		iv := intervals[intervalIdx]
		if lineNum < iv[0] {
			continue
		}
		if !emitting && len(results) > 0 {
			results = append(results, "--")
		}
		emitting = true
		sep := "-"
		if matchSet[lineNum] {
			sep = ":"
		}
		results = append(results, fmt.Sprintf("%s%s%d:%s", relPath, sep, lineNum, scanner.Text()))
	}
	return results, scanner.Err()
}

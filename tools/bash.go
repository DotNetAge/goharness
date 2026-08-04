package tools

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"mvdan.cc/sh/v3/syntax"
)

var (
	defaultWhitelistOnce sync.Once
	cachedWhitelist      []string
)

// defaultBashTimeoutMs 是 Bash 工具的默认超时时间（毫秒）。
// 默认 30 秒，可通过 timeout 参数覆盖。
const defaultBashTimeoutMs = 30000

// maxBashOutputSize 是 Bash 输出的最大字符数（stdout 和 stderr 分别计算）。
// 超过此限制的输出会被截断，并在结果中标记截断状态与缩小输出的指引，
// 避免无节制的输出进入上下文。
const maxBashOutputSize = 30000

// maxBashCommandLength 是 Bash 命令的最大字符数。
// 正常命令（含复杂管道与脚本）极少超过 16K 字符；超过此长度的命令
// 极大概率是通过 cat heredoc、echo 重定向等方式写入文件内容，
// 此类操作应使用 Write / Edit 工具完成，因此直接拒绝并引导。
const maxBashCommandLength = 16000

// BashTool 实现了 Shell 命令执行工具。
// 提供安全的命令执行环境，具有以下安全特性：
//   - 危险命令检测：阻止破坏性命令（如 rm -rf /、fork bomb 等）
//   - 命令白名单：只允许预定义的安全命令执行
//   - 超时控制：防止命令无限运行
//   - 输出限制：防止大量输出导致内存问题
//
// 安全级别：LevelHighRisk（高风险），因为可以执行任意 shell 命令
type BashTool struct {
	whitelistEnabled bool            // 是否启用白名单检查
	customWhitelist  map[string]bool // 自定义白名单（如果为空则使用默认白名单）
}

// NewBashTool 创建一个启用默认白名单的 Bash 工具实例。
// 使用内置的默认命令白名单，包含常用的安全命令。
//
// 返回：
//   - FuncTool: 配置好的 Bash 工具实例
func NewBashTool() FuncTool {
	return &BashTool{
		whitelistEnabled: true,
		customWhitelist:  make(map[string]bool),
	}
}

// NewBashToolWithWhitelist 创建一个使用自定义白名单的 Bash 工具。
//
// 参数：
//   - allowedCommands: 允许执行的命令名称列表
//
// 返回：
//   - FuncTool: 配置好的 Bash 工具实例
//
// 示例：
//
//	bash := NewBashToolWithWhitelist([]string{"git", "npm", "node"})
func NewBashToolWithWhitelist(allowedCommands []string) FuncTool {
	wl := make(map[string]bool)
	for _, cmd := range allowedCommands {
		wl[cmd] = true
	}
	return &BashTool{
		whitelistEnabled: true,
		customWhitelist:  wl,
	}
}

// NewBashToolUnrestricted 创建一个无白名单限制的 Bash 工具（不推荐用于生产环境）。
// 此模式允许执行任何命令，仅进行危险命令检测。
//
// ⚠️ 警告：此模式存在安全风险，应仅在受信任的环境中使用。
//
// 返回：
//   - FuncTool: 无白名单限制的 Bash 工具实例
func NewBashToolUnrestricted() FuncTool {
	return &BashTool{
		whitelistEnabled: false,
		customWhitelist:  make(map[string]bool),
	}
}

// Info 返回 Bash 工具的元信息。
// 包含工具名称、描述、参数定义、安全级别等。
func (t *BashTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Bash",
		MaxResultSizeChars: 60000,
		Description:        "在工作区环境中执行 POSIX shell 命令。在 Linux/proot 环境下，环境可扩展——可以安装软件包。在 macOS 上，命令通过 /bin/bash 原生运行。",
		Prompt: `执行给定的 bash 命令并返回其输出。

**重要**：以下任务请使用专用工具不要使用Bash命令：
- 文件搜索：使用 Glob（不要用 find）
- 内容搜索：使用 Grep（不要用 grep）
- 读取文件：使用 Read（不要用 cat/head/tail）
- 编辑文件：使用 Edit（不要用 sed/awk）
- 写入文件：使用 Write（不要用 tee/cat）

- 如果命令将创建新目录或文件，先用 Ls 验证父目录存在。
- 始终用双引号引用包含空格的文件路径。`,
		Tags:          []string{"shell", "execute", "system", "command", "process"},
		SecurityLevel: events.LevelHighRisk,
		Parameters: []Parameter{
			{
				Name:        "command",
				Type:        "string",
				Description: "要执行的命令。",
				Required:    true,
			},
			{
				Name:        "timeout",
				Type:        "number",
				Description: "可选的超时时间（毫秒）。默认为 30000 毫秒。",
				Required:    false,
			},
			{
				Name:        "working_dir",
				Type:        "string",
				Description: "命令执行的工作目录。默认为当前会话的项目目录（ProjectDir）。设置为 ProjectDir 之外的绝对路径以在其他目录下执行。",
				Required:    false,
			},
		},
	}
}

// generateKeyVariants 根据规范键名（小写+下划线格式）生成所有常见命名约定的变体，
// 用于实现大小写和命名风格不敏感的参数查找。
//
// 例如，输入 "working_dir" 会生成：
//   - WORKING_DIR（全大写+下划线）
//   - workingdir（全小写无分隔符）
//   - WORKINGDIR（全大写无分隔符）
//   - working-dir（小写+连字符）
//   - WORKING-DIR（大写+连字符）
//   - WorkingDir（大驼峰 PascalCase）
//   - workingDir（小驼峰 camelCase）
func GenerateKeyVariants(key string) []string {
	parts := strings.Split(key, "_")
	n := len(parts)

	variants := make([]string, 0, 8)

	// 全大写+下划线: WORKING_DIR
	variants = append(variants, strings.ToUpper(key))

	// 全小写无分隔符: workingdir
	lowerJoined := strings.Join(parts, "")
	variants = append(variants, lowerJoined)

	// 全大写无分隔符: WORKINGDIR
	variants = append(variants, strings.ToUpper(lowerJoined))

	// 小写+连字符: working-dir
	variants = append(variants, strings.Join(parts, "-"))

	// 大写+连字符: WORKING-DIR
	upperParts := make([]string, n)
	for i, p := range parts {
		upperParts[i] = strings.ToUpper(p)
	}
	variants = append(variants, strings.Join(upperParts, "-"))

	// 大驼峰 PascalCase: WorkingDir
	pascalParts := make([]string, n)
	for i, p := range parts {
		if len(p) > 0 {
			pascalParts[i] = strings.ToUpper(p[:1]) + p[1:]
		} else {
			pascalParts[i] = p
		}
	}
	variants = append(variants, strings.Join(pascalParts, ""))

	// 小驼峰 camelCase: workingDir（多词时才与 PascalCase 不同）
	if n > 0 {
		camelParts := make([]string, n)
		camelParts[0] = parts[0]
		for i := 1; i < n; i++ {
			if len(parts[i]) > 0 {
				camelParts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			} else {
				camelParts[i] = parts[i]
			}
		}
		variants = append(variants, strings.Join(camelParts, ""))
	}

	return variants
}

// getParam 从 params 中按多种命名约定提取参数值，提供大小写和命名风格不敏感的鲁棒匹配。
// 优先精确匹配 key，再尝试 generateKeyVariants 生成的所有变体。
//
// 支持的命名格式包括：
//   - 原始格式（如 "working_dir"）
//   - 全大写+下划线（如 "WORKING_DIR"）
//   - 全小写无分隔符（如 "workingdir"）
//   - 全大写无分隔符（如 "WORKINGDIR"）
//   - 小写+连字符（如 "working-dir"）
//   - 大写+连字符（如 "WORKING-DIR"）
//   - 大驼峰（如 "WorkingDir"）
//   - 小驼峰（如 "workingDir"）
//
// 参数：
//   - params: 参数映射
//   - key: 规范形式的键名（小写+下划线，如 "working_dir"）
//
// 返回：
//   - any: 找到的参数值
//   - bool: 是否找到匹配的键
func GetParam(params map[string]any, key string) (any, bool) {
	if val, ok := params[key]; ok {
		return val, true
	}
	for _, variant := range GenerateKeyVariants(key) {
		if val, ok := params[variant]; ok {
			return val, true
		}
	}
	return nil, false
}

// bashParams 承载 Bash 工具解析后的参数。
type bashParams struct {
	command    string
	timeoutMs  int
	workingDir string
}

// validateBashParams 从参数映射提取并验证 Bash 工具参数。
// 命令必须为非空字符串；超时参数被限制在 [1000, 300000] 毫秒范围内。
func validateBashParams(params map[string]any) (bashParams, error) {
	rawCommand, ok := GetParam(params, "command")
	if !ok {
		return bashParams{}, fmt.Errorf("%s", GuideMissingParam("Bash", "command"))
	}
	command, ok := rawCommand.(string)
	if !ok {
		return bashParams{}, fmt.Errorf("%s", GuideWrongParamType("Bash", "command", "string", rawCommand))
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return bashParams{}, fmt.Errorf("%s", GuideMissingParam("Bash", "command"))
	}

	timeoutMs := defaultBashTimeoutMs
	if rawTimeout, ok := GetParam(params, "timeout"); ok {
		if val, ok := rawTimeout.(float64); ok {
			timeoutMs = int(val)
			if timeoutMs < 1000 {
				timeoutMs = 1000
			}
			if timeoutMs > 300000 {
				timeoutMs = 300000
			}
		}
	}

	workingDir := ""
	if rawWd, ok := GetParam(params, "working_dir"); ok {
		if s, ok := rawWd.(string); ok {
			workingDir = s
		}
	}

	return bashParams{command: command, timeoutMs: timeoutMs, workingDir: workingDir}, nil
}

// performBash 执行命令核心逻辑：构建命令、解析工作目录、运行命令、收集输出与结果构建。
func performBash(ctx context.Context, logger logging.Logger, sessionID string, p bashParams) (map[string]any, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(p.timeoutMs)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(timeoutCtx, "cmd", "/c", p.command)
	} else {
		cmd = exec.CommandContext(timeoutCtx, "sh", "-c", p.command)
	}

	// 工作目录解析
	if p.workingDir != "" {
		// 统一路径解析：绝对路径化 + ~ 展开 + 相对项目目录解析。
		// 修复前 "~/workspaces" 直接 filepath.Clean 不会展开 ~，导致目录不存在。
		var projectDir string
		if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
			projectDir = tc.Session.ProjectDir()
		}
		cmd.Dir, _ = ResolveTargetPath(p.workingDir, projectDir, "")
	} else {
		// 默认以 ProjectDir 为工作目录
		if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
			cmd.Dir = tc.Session.ProjectDir()
		}
	}

	logger.Info("执行 Bash 命令",
		"command", truncateForLog(p.command, 200),
		"session_id", sessionID,
		"timeout_ms", p.timeoutMs,
		"working_dir", cmd.Dir,
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	elapsed := time.Since(startTime)

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	result := map[string]any{
		"stdout":      stdoutStr,
		"stderr":      stderrStr,
		"exit_code":   0,
		"interrupted": false,
	}

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			result["exit_code"] = exitError.ExitCode()
		} else if timeoutCtx.Err() == context.DeadlineExceeded {
			result["interrupted"] = true
			// 统一写入 stderrStr 变量，避免后续截断覆盖。
			stderrStr += "\n命令执行超时。请考虑缩短命令执行时间（缩小处理范围或使用更精确的命令），或将长任务放入后台执行。"
		} else {
			// 统一写入 stderrStr 变量，避免后续截断覆盖 err.Error()。
			stderrStr += "\n" + BuildGuide(
				fmt.Sprintf("尝试执行命令 %q，但命令未能启动", p.command),
				WithErrDetail("命令无法启动（解释器/可执行文件不存在或不可执行）", err),
				"确认命令引用的可执行文件存在且可执行（可用 which 验证），或检查命令路径是否拼写正确",
			)
			result["exit_code"] = -1
		}
	}

	var stdoutTruncated, stderrTruncated bool
	result["stdout"], stdoutTruncated = truncateOutput(stdoutStr, maxBashOutputSize)
	result["stderr"], stderrTruncated = truncateOutput(stderrStr, maxBashOutputSize)
	// 结构化标记截断状态，供 LLM 判断输出是否完整。
	result["stdout_truncated"] = stdoutTruncated
	result["stderr_truncated"] = stderrTruncated

	success := result["exit_code"] == 0
	result["success"] = success
	if !success {
		result["error"] = fmt.Sprintf("命令执行失败，退出码 %v", result["exit_code"])
		logger.Warn("Bash 命令执行失败",
			"exit_code", result["exit_code"],
			"elapsed_ms", elapsed.Milliseconds(),
			"stderr_len", len(stderrStr),
			"session_id", sessionID,
		)
	} else {
		logger.Debug("Bash 命令执行成功",
			"exit_code", 0,
			"elapsed_ms", elapsed.Milliseconds(),
			"stdout_len", len(stdoutStr),
			"session_id", sessionID,
		)
	}

	return result, nil
}

// Execute 编排 Bash 工具执行流程：validate → 长度检查 → enforce → perform。
func (t *BashTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	p, err := validateBashParams(params)
	if err != nil {
		return nil, err
	}

	logger := getLogger(ctx)

	if len(p.command) > maxBashCommandLength {
		logger.Warn("command 长度超过限制，拒绝执行",
			"length", len(p.command),
			"max", maxBashCommandLength,
		)
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试执行长度为 %d 字符的命令，超过 %d 字符的上限", len(p.command), maxBashCommandLength),
			"命令过长，极大概率是通过 cat heredoc、echo 重定向等方式写入文件内容，此类操作应使用专用工具完成",
			"如需写入文件内容请使用 Write 工具，修改文件请使用 Edit 工具（不要用 cat heredoc 或 echo 重定向绕过）；如需执行复杂逻辑，请将命令拆分后分步执行",
		))
	}

	// 安全强制检查：Grant 被绕过（如 PermissionAllow）时仍拦截危险命令与越权访问。
	// 沙箱启用时由沙箱统一检查（危险模式 + 白名单 + 网络命令 URL 预检）；
	// 沙箱未启用时走旧逻辑（detectDangerousCommand + isCommandWhitelisted）。
	if blocked, ok := t.enforceCommand(ctx, p.command); ok {
		return blocked, nil
	}

	sessionID := ExtractSessionID(ctx)
	return performBash(ctx, logger, sessionID, p)
}

// Grant 实现 tools.PermissionRequired 接口。在 Execute 运行前预检 shell 命令，
// 判断是否需要"询问用户"的信号。
//
// 沙箱启用时由沙箱统一做命令安全决策（CheckCommand + 网络命令 URL 预检）；
// 沙箱未启用时回退旧逻辑（detectDangerousCommand + isCommandWhitelisted）。
//
// 决策流程（沙箱启用）：
//  1. CheckCommand 危险模式检测 → Deny（硬性拒绝，授权不可覆盖）
//  2. CheckCommand 白名单检测 → 不在白名单则 AskUser
//  3. CheckCommand 网络命令 URL 预检 → NeedURLCheck=true 时对每个 URL 调用 CheckURL
//  4. AskUser 时检查会话级白名单（PermissionAllowSession 记忆）
//
// 注意：沙箱的 extractBaseCommand 只取首个命令，而旧逻辑用 AST 解析所有子命令。
// 为保留对子命令（如 "git status && rm -rf /"）的白名单检查能力，沙箱启用时
// 仍调用 isCommandWhitelisted 做 AskUser 决策的细化（仅当工具配置了 customWhitelist
// 或沙箱策略 AllowedDirs 为空时生效）。
func (t *BashTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	rawCommand, ok := GetParam(params, "command")
	command := ""
	if ok {
		command, _ = rawCommand.(string)
	}
	command = strings.TrimSpace(command)
	if command == "" {
		// 无命令时让 Execute 产生清晰的错误——无需就空调用询问用户。
		return true, ""
	}

	// 沙箱启用时，由沙箱统一做命令安全决策
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			return t.grantWithSandbox(ctx, sb, command)
		}
	}

	// 沙箱未启用，走旧逻辑
	if blocked := detectDangerousCommand(command); blocked != "" {
		return false, fmt.Sprintf("Bash 命令被安全过滤器阻止：%s", blocked)
	}

	if t.whitelistEnabled {
		if allowed, failedCmd := t.isCommandWhitelisted(command); !allowed {
			cmds := extractCommands(command)

			// 先检查会话级白名单（用户之前选择"记住本次会话"的授权）
			if tc := GetToolContext(ctx); tc != nil && tc.SessionWhitelist != nil {
				allInSession := true
				for _, cmd := range cmds {
					found := false
					for _, allowed := range tc.SessionWhitelist.Bash {
						if cmd == allowed {
							found = true
							break
						}
					}
					if !found {
						allInSession = false
						break
					}
				}
				if allInSession {
					return true, ""
				}
			}

			// 准确报告实际不在白名单中的命令
			blockedCmd := failedCmd
			if blockedCmd == "" && len(cmds) > 0 {
				blockedCmd = cmds[0]
			} else if blockedCmd == "" {
				blockedCmd = "unknown"
			}
			return false, fmt.Sprintf("Bash：命令 %q 不在白名单中", blockedCmd)
		}
	}

	return true, ""
}

// grantWithSandbox 在沙箱启用时做命令安全决策。
//
// 决策流程：
//  1. CheckCommand 返回 Deny（危险模式）→ 直接拒绝
//  2. CheckCommand 返回 AskUser（不在白名单）→ 检查会话级白名单；命中则继续 URL 预检，否则触发授权
//  3. CheckCommand 返回 Allow + NeedURLCheck（网络命令）→ 对每个 URL 调用 CheckURL
//  4. CheckCommand 返回 Allow → 放行
//
// 会话级白名单命中时仍需做 URL 预检，避免用户放行 curl 后 SSRF 被绕过。
func (t *BashTool) grantWithSandbox(ctx context.Context, sb *sandbox.Sandbox, command string) (bool, string) {
	dec := sb.CheckCommand(command)

	switch dec.Decision {
	case sandbox.DecisionDeny:
		return false, dec.Reason

	case sandbox.DecisionAskUser:
		// 检查会话级白名单（用户之前选择"记住本次会话"的授权）
		inSession := false
		if tc := GetToolContext(ctx); tc != nil && tc.SessionWhitelist != nil {
			cmds := extractCommands(command)
			allInSession := true
			for _, cmd := range cmds {
				found := false
				for _, allowed := range tc.SessionWhitelist.Bash {
					if cmd == allowed {
						found = true
						break
					}
				}
				if !found {
					allInSession = false
					break
				}
			}
			if allInSession && len(cmds) > 0 {
				inSession = true
			}
		}
		if !inSession {
			return false, dec.Reason
		}
		// 会话白名单命中，继续做 URL 预检（若有）
		// 命中会话白名单时 dec.NeedURLCheck 不会为 true（CheckCommand 仅在 Allow 时返回 NeedURLCheck），
		// 但网络命令仍需预检，这里主动提取 URL 检查。
		if reason := t.enforceNetworkURLs(sb, command); reason != "" {
			return false, reason
		}
		return true, ""

	case sandbox.DecisionAllow:
		// 网络命令 URL 预检
		if dec.NeedURLCheck {
			if reason := t.checkURLs(sb, dec.URLs); reason != "" {
				return false, reason
			}
		}
		return true, ""
	}

	return true, ""
}

// checkURLs 对 CheckCommand 提取的 URL 列表逐个做 CheckURL 预检。
// 返回非空字符串表示被拒原因，空字符串表示全部通过。
func (t *BashTool) checkURLs(sb *sandbox.Sandbox, urls []string) string {
	for _, u := range urls {
		dec := sb.CheckURL(u)
		if dec.Decision == sandbox.DecisionDeny {
			return dec.Reason
		}
	}
	return ""
}

// enforceNetworkURLs 主动从命令中提取 URL 并做 CheckURL 预检。
// 用于会话白名单命中后仍需 SSRF 防护的场景。
func (t *BashTool) enforceNetworkURLs(sb *sandbox.Sandbox, command string) string {
	urls := sandbox.ExtractURLsFromCommand(command)
	if len(urls) == 0 {
		return ""
	}
	return t.checkURLs(sb, urls)
}

// enforceCommand 是 Execute 阶段的强制安全检查。
//
// 返回值：
//   - result: 被拦截时返回阻塞结果 map（exit_code=126），未拦截时为 nil
//   - blocked: true 表示命令被拦截（调用方应返回 result），false 表示放行
//
// 沙箱启用时由沙箱统一检查（危险模式 + 白名单 + 网络命令 URL 预检），
// AskUser 在 Execute 阶段视为 Deny（与 Glob 的 CheckFileAllowOrDeny 语义一致）。
// 沙箱未启用时走旧逻辑（detectDangerousCommand + isCommandWhitelisted）。
func (t *BashTool) enforceCommand(ctx context.Context, command string) (map[string]any, bool) {
	// 沙箱启用时由沙箱统一检查
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			if reason := t.enforceWithSandbox(sb, command); reason != "" {
				return map[string]any{
					"stdout":      "",
					"stderr":      fmt.Sprintf("已阻止：%s", reason),
					"exit_code":   126,
					"interrupted": false,
					"success":     false,
					"error":       reason,
				}, true
			}
			return nil, false
		}
	}

	// 沙箱未启用，走旧逻辑
	if blocked := detectDangerousCommand(command); blocked != "" {
		return map[string]any{
			"stdout":      "",
			"stderr":      fmt.Sprintf("已阻止：%s", blocked),
			"exit_code":   126,
			"interrupted": false,
			"success":     false,
			"error":       blocked,
		}, true
	}
	if t.whitelistEnabled {
		if allowed, failedCmd := t.isCommandWhitelisted(command); !allowed {
			blockedCmd := failedCmd
			if blockedCmd == "" {
				blockedCmd = "unknown"
			}
			return map[string]any{
				"stdout":      "",
				"stderr":      fmt.Sprintf("已阻止：命令 %q 不在白名单中。允许的命令：%s", blockedCmd, t.whitelistDisplay()),
				"exit_code":   126,
				"interrupted": false,
				"success":     false,
				"error":       fmt.Sprintf("命令不在白名单中：%s", blockedCmd),
			}, true
		}
	}
	return nil, false
}

// enforceWithSandbox 是沙箱启用时的强制检查（简化决策路径：Allow/Deny，不返回 AskUser）。
// 返回非空字符串表示被拒原因，空字符串表示放行。
func (t *BashTool) enforceWithSandbox(sb *sandbox.Sandbox, command string) string {
	dec := sb.CheckCommand(command)
	switch dec.Decision {
	case sandbox.DecisionDeny:
		return dec.Reason
	case sandbox.DecisionAskUser:
		// Execute 阶段不弹窗，AskUser 视为 Deny
		return dec.Reason
	case sandbox.DecisionAllow:
		if dec.NeedURLCheck {
			if reason := t.checkURLs(sb, dec.URLs); reason != "" {
				return reason
			}
		}
		// 主动提取 URL 做 SSRF 预检（覆盖会话白名单放行的网络命令）
		if reason := t.enforceNetworkURLs(sb, command); reason != "" {
			return reason
		}
		return ""
	}
	return ""
}

// truncateOutput 截断字符串到指定字符数（按 rune 计算）。
// 如果字符串超过限制，会保留前 maxRunes 个字符，并在末尾追加
// 包含输出总量与缩小输出范围的指引信息。
//
// 参数：
//   - s: 原始字符串
//   - maxRunes: 最大字符数
//
// 返回：
//   - string: 截断后的字符串（如果未超限则返回原字符串）
//   - bool: 是否发生了截断
func truncateOutput(s string, maxRunes int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, false
	}
	shown := string(runes[:maxRunes])
	hint := fmt.Sprintf(
		"\n... [输出被截断：共 %d 个字符，仅显示前 %d 个字符。"+
			"请使用管道缩小输出范围后重试（如 | head -n 100、| tail -n 100、| grep '关键词'），"+
			"或将输出重定向到文件后使用 Read 工具分页读取。] ...",
		len(runes), maxRunes)
	return shown + hint, true
}

// truncateForLog 截断字符串用于安全日志记录。
// 防止在日志中记录过长的命令（可能包含敏感信息）。
//
// 参数：
//   - s: 原始字符串
//   - maxLen: 最大长度
//
// 返回：
//   - string: 截断后的字符串（如果未超限则返回原字符串）
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// dangerousPatterns 定义了危险命令的检测规则列表。
// 每个规则包含一个正则表达式和对应的危险原因描述。
//
// 检测的危险命令类型包括：
//   - 文件系统破坏：rm -rf /, dd 写入磁盘等
//   - 远程代码执行：curl | sh, 命令替换等
//   - 系统控制：shutdown, reboot 等
//   - 资源耗尽：fork bomb 等
//   - 权限修改：chmod 777 /, chown 递归等
var dangerousPatterns = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`^rm\s+-rf\s+/\s*$`), "破坏性：rm -rf / 会删除整个文件系统"},
	{regexp.MustCompile(`^rm\s+-rf\s+/\*\s*$`), "破坏性：rm -rf /* 会删除整个文件系统"},
	{regexp.MustCompile(`>\s*/dev/sd[a-z]\b`), "危险：写入原始磁盘设备"},
	{regexp.MustCompile(`dd\s+if=.*of=/dev/sd`), "危险：通过 dd 覆盖原始磁盘"},
	{regexp.MustCompile(`mkfs\.`), "危险：磁盘格式化命令"},
	{regexp.MustCompile(`:\(\)\{\s*\|.*:\s*&\s*;:\s*\}$`), "危险：检测到 fork bomb"},
	{regexp.MustCompile(`(curl|wget)\s+.*\|\s*(sh|bash)\b`), "危险：远程代码执行管道 (curl|sh)"},
	{regexp.MustCompile(`(curl|wget)\s+.*\s*>\s*/(bin|usr/bin)/`), "危险：远程下载二进制文件到系统路径"},
	{regexp.MustCompile(`chmod\s+-R\s+777\s+/`), "危险：根文件系统设置为全局可写"},
	{regexp.MustCompile(`chown\s+-R.*\s+/`), "危险：递归更改根目录所有权"},
	{regexp.MustCompile(`shutdown\s+(now|-h|-r)\b`), "危险：系统关机命令"},
	{regexp.MustCompile(`^reboot\s*$`), "危险：系统重启命令"},
	{regexp.MustCompile(`(?i)format\s+[a-z]:\s*/q`), "危险：Windows 磁盘格式化"},
	{regexp.MustCompile(`(?i)del\s+/[fs]\s+.*\\\*\.\*`), "危险：Windows 强制递归删除"},
	{regexp.MustCompile(`(?i)rd\s+/[fs]\s+\\`), "危险：Windows 强制删除目录"},
	{regexp.MustCompile(`(?i)reg\s+delete\s+HK`), "危险：注册表项删除"},
	{regexp.MustCompile(`(?i)diskpart`), "危险：磁盘分区操作"},
	{regexp.MustCompile(`(?i)icacls\s+.*\s+/grant\s+.*:F`), "危险：授予完全权限"},
	{regexp.MustCompile(`(?i)takeown\s+/f`), "危险：获取文件所有权"},
	{regexp.MustCompile(`(?i)cipher\s+/w:`), "危险：磁盘擦除操作"},
	{regexp.MustCompile(`(?i)bcdedit\s+/set`), "危险：启动配置修改"},
	{regexp.MustCompile(`(?i)powercfg\s+/h\s+off`), "危险：系统配置修改"},
	{regexp.MustCompile(`(?i)net\s+user\s+.*\s+/add`), "危险：创建用户账户"},
	{regexp.MustCompile(`(?i)net\s+localgroup\s+.*\s+/add`), "危险：修改用户组"},
	{regexp.MustCompile(`(?i)sc\s+delete`), "危险：删除服务"},
}

// detectDangerousCommand 检测命令是否包含危险操作。
// 遍历所有危险模式进行匹配，返回第一个匹配的原因描述。
//
// 参数：
//   - command: 要检测的 Shell 命令
//
// 返回：
//   - string: 如果匹配到危险模式，返回原因描述；否则返回空字符串
func detectDangerousCommand(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, dp := range dangerousPatterns {
		if dp.pattern.MatchString(lower) {
			return dp.reason
		}
	}
	return ""
}

// getDefaultWhitelist 返回默认的允许命令列表。
// 这些命令被认为是安全的，可以正常执行。
//
// 列表包含：
//   - 文件操作：ls, cp, mv, mkdir, rm 等
//   - 版本控制：git, svn, hg
//   - 编程语言：python, node, go, cargo 等
//   - 构建工具：make, cmake, gcc 等
//   - 容器编排：docker, kubectl, helm
//   - 网络工具：curl, wget, ssh 等
//   - 系统工具：ps, top, df, du 等
func getDefaultWhitelist() []string {
	defaultWhitelistOnce.Do(func() {
		cachedWhitelist = buildDefaultWhitelist()
	})
	return cachedWhitelist
}

// buildDefaultWhitelist 构建默认白名单列表（仅在首次调用时执行一次）。
func buildDefaultWhitelist() []string {
	baseCmds := []string{
		"cat", "echo", "head", "tail", "less", "more",
		"ls", "wc", "pwd", "cd", "mkdir", "touch", "cp", "mv", "rm",
		"chmod", "chown", "ln", "tar", "gzip", "gunzip", "zip", "unzip",
		"git", "svn", "hg",
		"python", "python3", "pip", "pip3", "node", "npm", "cnpm", "npx", "nvm",
		"go", "cargo", "rustc",
		"make", "cmake", "gcc", "g++", "clang", "clang++",
		"docker", "docker-compose", "kubectl", "helm",
		"curl", "wget", "ssh", "scp", "rsync",
		"ps", "top", "htop", "kill", "killall", "pgrep", "pkill", "mindx",
		"df", "du", "free", "uname", "date", "whoami", "id",
		"env", "export", "source", "alias", "which", "type", "file",
		"sort", "uniq", "cut", "tr", "tee", "xargs",
		"grep", "rg", "find", "diff", "comm",
		"awk", "sed", "printf",
		"jq", "yq",
		"test", "[[", "true", "false", "exit", "return",
		// Shell 语法关键字（非外部命令，不会产生进程）
		"for", "while", "until", "if", "then", "else", "elif", "fi",
		"case", "esac", "do", "done", "select", "function",
		"sleep", "wait", "bg", "fg", "jobs", "nohup", "disown",
		"basename", "dirname", "realpath", "readlink",
		"sha256sum", "md5sum", "sha1sum", "shasum",
		"openssl", "gpg", "ssh-keygen",
		"time", "timeout", "watch",
		"hostname", "uptime", "lscpu", "lsblk",
		"ping", "ss", "dig", "host", "nslookup", "traceroute", "ip",
		"lsof",
		"strings", "xxd", "od", "column", "seq", "shuf", "fmt", "nl", "fold",
		"ldd", "nm", "objdump", "readelf", "size", "strip",
	}

	if runtime.GOOS == "windows" {
		baseCmds = append(baseCmds,
			"dir", "type", "findstr", "where", "tasklist", "taskkill",
			"systeminfo", "ver", "netstat", "ipconfig", "ping", "tracert",
			"nslookup", "route", "netsh", "powershell", "pwsh",
			"attrib", "cacls", "icacls", "compact", "chkdsk",
			"fc", "comp", "more", "sort", "tree", "xcopy", "robocopy",
			"cscript", "wscript", "mshta",
		)
	}

	return baseCmds
}

// whitelistDisplay 返回活跃白名单的可读字符串（用于错误信息展示）。
// 优先使用自定义白名单，否则返回默认白名单。
func (t *BashTool) whitelistDisplay() string {
	if len(t.customWhitelist) > 0 {
		cmds := make([]string, 0, len(t.customWhitelist))
		for cmd := range t.customWhitelist {
			cmds = append(cmds, cmd)
		}
		sort.Strings(cmds)
		return strings.Join(cmds, ", ")
	}
	return strings.Join(getDefaultWhitelist(), ", ")
}

// extractCommands 从 Shell 命令字符串中提取所有真实的命令名。
// 使用 mvdan/sh 的 AST 解析器准确解析 shell 语法，避免正则表达式的误匹配。
//
// 例如："for i in 1 2 3; do rm -rf /tmp; done" → ["rm"]
//
//	"if [ -f file ]; then cat file; fi" → ["cat"]
//	"git status && cargo build" → ["git", "cargo"]
func extractCommands(command string) []string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}

	file, err := syntax.NewParser(syntax.KeepComments(false)).Parse(strings.NewReader(trimmed), "")
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var cmds []string

	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if len(call.Args[0].Parts) == 0 {
			return true
		}
		lit, ok := call.Args[0].Parts[0].(*syntax.Lit)
		if !ok {
			return true
		}
		cmd := lit.Value
		// 跳过 [ 和 [[ 这种内置 test 命令，它们不是用户意图中的"真实命令"
		if cmd == "[" || cmd == "[[" {
			return true
		}
		if !seen[cmd] {
			seen[cmd] = true
			cmds = append(cmds, cmd)
		}
		return true
	})

	return cmds
}

// isCommandWhitelisted 检查命令（及其所有子命令）是否在白名单中。
// 优先检查自定义白名单，如果为空则使用默认白名单。
//
// 参数：
//   - command: 要检查的 Shell 命令
//
// 返回：
//   - bool: 如果所有子命令都在白名单中返回 true，否则返回 false
//   - string: 第一个不在白名单中的命令名（如果全部允许则为空字符串）
func (t *BashTool) isCommandWhitelisted(command string) (bool, string) {
	cmds := extractCommands(command)
	if len(cmds) == 0 {
		return false, ""
	}

	// 构建白名单 map
	var wl map[string]bool
	if len(t.customWhitelist) > 0 {
		wl = t.customWhitelist
	} else {
		defaultWL := getDefaultWhitelist()
		wl = make(map[string]bool, len(defaultWL))
		for _, c := range defaultWL {
			wl[c] = true
		}
	}

	for _, cmd := range cmds {
		if !wl[cmd] {
			return false, cmd
		}
	}
	return true, ""
}

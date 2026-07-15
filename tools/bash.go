package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/events"
)

var (
	defaultWhitelistOnce sync.Once
	cachedWhitelist      []string
)

// defaultBashTimeoutMs 是 Bash 工具的默认超时时间（毫秒）。
// 默认 30 秒，可通过 timeout 参数覆盖。
const defaultBashTimeoutMs = 30000

// maxBashOutputSize 是 Bash 输出的最大字符数（stdout 和 stderr 分别计算）。
// 超过此限制的输出会被截断，防止上下文爆炸。
const maxBashOutputSize = 30000

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

// baseCommandPattern 是用于提取命令基础名称的正则表达式。
// 匹配命令行开头的字母数字命令名（如 ls, git, npm 等）。
var baseCommandPattern = regexp.MustCompile(`^\s*([a-zA-Z][a-zA-Z0-9._\-]*)(\s|$)`)

// shellKeywords are shell built-in control words that are not external commands.
// Used to skip keyword-only segments when extracting real commands from compound statements.
var shellKeywords = map[string]bool{
	"for": true, "while": true, "until": true, "if": true, "then": true,
	"else": true, "elif": true, "fi": true, "do": true, "done": true,
	"case": true, "esac": true, "select": true, "function": true,
	"in": true, "!": true, "{": true, "}": true,
}

// Info 返回 Bash 工具的元信息。
// 包含工具名称、描述、参数定义、安全级别等。
func (t *BashTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Bash",
		MaxResultSizeChars: 60000,
		Description:        "在工作区环境中执行 POSIX shell 命令。在 Linux/proot 环境下，环境可扩展——可以安装软件包。在 macOS 上，命令通过 /bin/bash 原生运行。",
		Prompt: `执行给定的 bash 命令并返回其输出。

**重要**：以下命令会被 Bash 工具白名单阻止——请使用专用工具：
- 文件搜索：使用 Glob（不要用 find）
- 内容搜索：使用 Grep
- 读取文件：使用 Read（不要用 cat/head/tail）
- 编辑文件：使用 Edit（不要用 sed/awk）
- 写入文件：使用 Write（不要用 tee/cat）

专用工具提供更好的体验，也更容易审查工具调用。

- 如果命令将创建新目录或文件，先用 Ls 验证父目录存在。
- 始终用双引号引用包含空格的文件路径。
- 使用 working_dir 在特定目录中运行命令。`,
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

// Execute 执行 Shell 命令。
//
// 执行流程：
//  1. 验证命令参数（必须为非空字符串）
//  2. 检查命令长度限制（最大 100000 字符）
//  3. 危险命令检测（阻止破坏性操作）
//  4. 白名单检查（如果启用）
//  5. 解析超时参数（默认 30 秒，最小 1 秒，最大 5 分钟）
//  6. 通过 sh -c 执行命令
//  7. 收集输出并返回结构化结果
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "command" 键，可选 "timeout" 和 "working_dir"
//
// 返回：
//   - map[string]any: 包含 stdout、stderr、exit_code、success 等字段
//   - error: 仅在参数验证失败时返回错误，执行错误在 result 中
func (t *BashTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	command, ok := params["command"].(string)
	if !ok {
		return nil, fmt.Errorf("缺少 command 参数")
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("缺少 command 参数")
	}

	logger := getLogger(ctx)
	sessionID := ExtractSessionID(ctx)

	if len(command) > 100000 {
		logger.Warn("command 长度超过 100K 字符，拒绝执行",
			"length", len(command),
			"max", 100000,
		)
		return nil, fmt.Errorf("command 长度超过 100K 字符，拒绝执行")
	}

	if blocked := detectDangerousCommand(command); blocked != "" {
		return map[string]any{
			"stdout":      "",
			"stderr":      fmt.Sprintf("BLOCKED: %s", blocked),
			"exit_code":   126,
			"interrupted": false,
			"success":     false,
			"error":       blocked,
		}, nil
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
			}, nil
		}
	}

	timeoutMs := defaultBashTimeoutMs
	if val, ok := params["timeout"].(float64); ok {
		timeoutMs = int(val)
		if timeoutMs < 1000 {
			timeoutMs = 1000
		}
		if timeoutMs > 300000 {
			timeoutMs = 300000
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(timeoutCtx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(timeoutCtx, "sh", "-c", command)
	}

	if wd, ok := params["working_dir"].(string); ok && wd != "" {
		cmd.Dir = filepath.Clean(wd)
	} else {
		// 默认以 ProjectDir 为工作目录
		if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
			cmd.Dir = tc.Session.ProjectDir()
		}
	}

	logger.Info("执行 Bash 命令",
		"command", truncateForLog(command, 200),
		"session_id", sessionID,
		"timeout_ms", timeoutMs,
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
			stderrStr += "\n命令执行超时。"
			result["stderr"] = stderrStr
		} else {
			result["stderr"] = stderrStr + "\n" + err.Error()
			result["exit_code"] = -1
		}
	}

	result["stdout"] = truncateOutput(stdoutStr, maxBashOutputSize)
	result["stderr"] = truncateOutput(stderrStr, maxBashOutputSize)

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

// Grant implements tools.PermissionRequired. It pre-checks the shell command
// for any "ask the user" signals BEFORE Execute runs:
//
//  1. Hard-coded dangerous patterns (rm -rf /, fork bombs, curl|sh, etc.)
//     → refuse; user override is NOT possible via Grant, but the dangerous
//     list is just a safety net. Execute() also blocks these independently
//     (so they cannot be reached even if Grant is bypassed).
//  2. Whitelist: if whitelistEnabled, the base command must be in the
//     (default or custom) whitelist. Anything outside the whitelist is
//     "ask the user" — the user is the source of truth, not a regex list.
//
// Anything that passes both checks is "obviously safe enough to just run"
// and Grant returns granted=true. The runtime will then proceed to Execute.
func (t *BashTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	command, _ := params["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		// No command? Let Execute produce a clean error — no point asking
		// the user about an empty invocation.
		return true, ""
	}

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

// truncateOutput 截断字符串到指定字符数（按 rune 计算）。
// 如果字符串超过限制，会在末尾附加截断提示信息。
//
// 参数：
//   - s: 原始字符串
//   - maxRunes: 最大字符数
//
// 返回：
//   - string: 截断后的字符串（如果未超限则返回原字符串）
func truncateOutput(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "\n... [输出因大小限制被截断] ..."
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
	{regexp.MustCompile(`\$\(`), "危险：通过 $() 进行命令替换"},
	{regexp.MustCompile("`[^`]*`"), "危险：通过反引号进行命令替换"},
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
		"docker", "kubectl", "helm",
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
// 处理复合命令（for/while/if/case），提取 do / then / ; / && / || 后面的真正命令。
// 例如："for i in 1 2 3; do rm -rf /tmp; done" → ["rm"]
//
//	"if [ -f file ]; then cat file; fi" → ["[", "cat"]
//	"git status && cargo build" → ["git", "cargo"]
func extractCommands(command string) []string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}

	// 第一层：按逻辑分隔符 && || ; 分割（保留管道 | 单独处理）
	logicSep := regexp.MustCompile(`&&|\|\||;`)
	logicSegments := logicSep.Split(trimmed, -1)

	var cmds []string
	seen := make(map[string]bool)
	for _, seg := range logicSegments {
		// 第二层：按管道 | 分割
		pipeSegments := strings.Split(seg, "|")
		for _, ps := range pipeSegments {
			ps = strings.TrimSpace(ps)
			if ps == "" {
				continue
			}
			// 持续跳过 shell 关键字，提取每个管道分段中的第一个真实命令
			for {
				m := baseCommandPattern.FindStringSubmatch(ps)
				if len(m) < 2 {
					break
				}
				cmd := m[1]
				ps = strings.TrimSpace(ps[len(m[0]):])

				if shellKeywords[cmd] {
					if cmd == "for" {
						// for 循环变量名不是命令，跳过
						if m2 := baseCommandPattern.FindStringSubmatch(ps); len(m2) >= 2 {
							ps = strings.TrimSpace(ps[len(m2[0]):])
						}
					}
					continue // 跳过关键字，继续查找同一分段的下一个命令
				}
				if seen[cmd] {
					break // 已处理过该命令
				}
				seen[cmd] = true
				cmds = append(cmds, cmd)
				break // 每个管道分段只提取第一个真实命令
			}
		}
	}
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

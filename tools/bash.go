package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/events"
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
	whitelistEnabled bool             // 是否启用白名单检查
	customWhitelist  map[string]bool  // 自定义白名单（如果为空则使用默认白名单）
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

// Info 返回 Bash 工具的元信息。
// 包含工具名称、描述、参数定义、安全级别等。
func (t *BashTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "Bash",
		MaxResultSizeChars: 60000,
		Description:        "Execute a POSIX shell command in the workspace environment. On Linux/proot, the environment is extensible — packages can be installed. On macOS, commands run natively via /bin/bash.",
		Prompt: `Executes a given bash command and returns its output. The project directory persists between commands, but shell state does not.

IMPORTANT: The following commands are blocked by the Bash tool whitelist — use dedicated tools instead:
- File search: Use Glob (NOT find)
- Content search: Use Grep
- Read files: Use Read (NOT cat/head/tail)
- Edit files: Use FileEdit (NOT sed/awk)
- Write files: Use Write

Dedicated tools provide a better user experience and make it easier to review tool calls.

# Instructions
- If your command will create new directories or files, first use Ls to verify the parent directory exists.
- Always quote file paths that contain spaces with double quotes.
- Try to maintain your current project directory by using absolute paths.
- When issuing multiple commands:
  - If independent and can run in parallel, make multiple tool calls in one message.
  - If dependent, use && to chain them.
  - Use ; only when you don't care if earlier commands fail.
   - DO NOT use newlines to separate commands (newlines are ok in quoted strings).
- For git commands:
   - Prefer new commits over amending existing ones.
   - Before destructive operations (git reset --hard, git push --force), consider safer alternatives.
- Use working_dir to run commands in a specific directory.`,
		Tags:          []string{"shell", "execute", "system", "command", "process"},
		SecurityLevel: events.LevelHighRisk,
		Parameters: []Parameter{
			{
				Name:        "command",
				Type:        "string",
				Description: "The command to execute.",
				Required:    true,
			},
			{
				Name:        "timeout",
				Type:        "number",
				Description: "Optional timeout in milliseconds. Default is 30000ms.",
				Required:    false,
			},
			{
				Name:        "working_dir",
				Type:        "string",
				Description: "Working directory for command execution. Defaults to the process current directory.",
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
		return nil, fmt.Errorf("missing command parameter")
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("empty command parameter")
	}

	logger := getLogger(ctx)
	sessionID := ExtractSessionID(ctx)

	if len(command) > 100000 {
		logger.Warn("command exceeds maximum length",
			"length", len(command),
			"max", 100000,
		)
		return nil, fmt.Errorf("command exceeds maximum length of 100000 characters")
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
		if allowed := t.isCommandWhitelisted(command); !allowed {
			baseCmd := extractBaseCommand(command)
			return map[string]any{
				"stdout":      "",
				"stderr":      fmt.Sprintf("BLOCKED: command %q is not in the whitelist. Allowed commands: %s", baseCmd, strings.Join(getDefaultWhitelist(), ", ")),
				"exit_code":   126,
				"interrupted": false,
				"success":     false,
				"error":       fmt.Sprintf("command not whitelisted: %s", baseCmd),
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
		wd = filepath.Clean(wd)
		cmd.Dir = wd
	}

	logger.Info("executing bash command",
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
			stderrStr += "\nCommand timed out."
			result["stderr"] = stderrStr
		} else {
			result["stderr"] = stderrStr + "\n" + err.Error()
			result["exit_code"] = -1
		}
	}

	const maxOutputSize = maxBashOutputSize
	result["stdout"] = truncateOutput(stdoutStr, maxOutputSize)
	result["stderr"] = truncateOutput(stderrStr, maxOutputSize)

	result["success"] = result["exit_code"] == 0
	if !result["success"].(bool) {
		result["error"] = fmt.Sprintf("Command failed with exit code %v", result["exit_code"])
		logger.Warn("bash command failed",
			"exit_code", result["exit_code"],
			"elapsed_ms", elapsed.Milliseconds(),
			"stderr_len", len(stderrStr),
			"session_id", sessionID,
		)
	} else {
		logger.Debug("bash command completed",
			"exit_code", 0,
			"elapsed_ms", elapsed.Milliseconds(),
			"stdout_len", len(stdoutStr),
			"session_id", sessionID,
		)
	}

	return result, nil
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
	return string(runes[:maxRunes]) + "\n... [output truncated due to size] ..."
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
	{regexp.MustCompile(`^rm\s+-rf\s+/\s*$`), "destructive: rm -rf / would erase the entire filesystem"},
	{regexp.MustCompile(`^rm\s+-rf\s+/\*\s*$`), "destructive: rm -rf /* would erase the entire filesystem"},
	{regexp.MustCompile(`>\s*/dev/sd[a-z]\b`), "dangerous: writing to raw disk device"},
	{regexp.MustCompile(`dd\s+if=.*of=/dev/sd`), "dangerous: raw disk overwrite via dd"},
	{regexp.MustCompile(`mkfs\.`), "dangerous: disk formatting command"},
	{regexp.MustCompile(`:\(\)\{\s*\|.*:\s*&\s*;:\s*\}$`), "dangerous: fork bomb detected"},
	{regexp.MustCompile(`(curl|wget)\s+.*\|\s*(sh|bash)\b`), "dangerous: remote code execution pipe (curl|sh)"},
	{regexp.MustCompile(`\$\(`), "dangerous: command substitution via $()"},
	{regexp.MustCompile("`[^`]*`"), "dangerous: command substitution via backticks"},
	{regexp.MustCompile(`(curl|wget)\s+.*\s*>\s*/(bin|usr/bin)/`), "dangerous: remote binary download to system path"},
	{regexp.MustCompile(`chmod\s+-R\s+777\s+/`), "dangerous: world-writable root filesystem"},
	{regexp.MustCompile(`chown\s+-R.*\s+/`), "dangerous: recursive root ownership change"},
	{regexp.MustCompile(`shutdown\s+(now|-h|-r)\b`), "dangerous: system shutdown command"},
	{regexp.MustCompile(`^reboot\s*$`), "dangerous: system reboot command"},
	{regexp.MustCompile(`(?i)format\s+[a-z]:\s*/q`), "dangerous: disk formatting on Windows"},
	{regexp.MustCompile(`(?i)del\s+/[fs]\s+.*\\\*\.\*`), "dangerous: forced recursive deletion on Windows"},
	{regexp.MustCompile(`(?i)rd\s+/[fs]\s+\\`), "dangerous: forced directory deletion on Windows"},
	{regexp.MustCompile(`(?i)reg\s+delete\s+HK`), "dangerous: registry key deletion"},
	{regexp.MustCompile(`(?i)diskpart`), "dangerous: disk partition manipulation"},
	{regexp.MustCompile(`(?i)icacls\s+.*\s+/grant\s+.*:F`), "dangerous: granting full permissions"},
	{regexp.MustCompile(`(?i)takeown\s+/f`), "dangerous: taking ownership of files"},
	{regexp.MustCompile(`(?i)cipher\s+/w:`), "dangerous: disk wiping operation"},
	{regexp.MustCompile(`(?i)bcdedit\s+/set`), "dangerous: boot configuration modification"},
	{regexp.MustCompile(`(?i)powercfg\s+/h\s+off`), "dangerous: system configuration modification"},
	{regexp.MustCompile(`(?i)net\s+user\s+.*\s+/add`), "dangerous: user account creation"},
	{regexp.MustCompile(`(?i)net\s+localgroup\s+.*\s+/add`), "dangerous: user group modification"},
	{regexp.MustCompile(`(?i)sc\s+delete`), "dangerous: service deletion"},
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
	baseCmds := []string{
		"ls", "wc", "pwd", "cd", "mkdir", "touch", "cp", "mv", "rm",
		"chmod", "chown", "ln", "tar", "gzip", "gunzip", "zip", "unzip",
		"git", "svn", "hg",
		"python", "python3", "pip", "pip3", "node", "npm", "npx",
		"go", "cargo", "rustc",
		"make", "cmake", "gcc", "g++", "clang", "clang++",
		"docker", "kubectl", "helm",
		"curl", "wget", "ssh", "scp", "rsync",
		"ps", "top", "htop", "kill", "killall", "pgrep", "pkill",
		"df", "du", "free", "uname", "date", "whoami", "id",
		"env", "export", "source", "alias", "which", "type", "file",
		"sort", "uniq", "cut", "tr", "tee", "xargs",
		"jq", "yq",
		"test", "[[", "true", "false", "exit", "return",
		"sleep", "wait", "bg", "fg", "jobs", "nohup", "disown",
		"basename", "dirname", "realpath", "readlink",
		"sha256sum", "md5sum", "sha1sum", "shasum",
		"openssl", "gpg", "ssh-keygen",
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

// extractBaseCommand 从 Shell 命令字符串中提取基础命令名。
// 例如："git status" → "git", "npm install" → "npm"
//
// 参数：
//   - command: 完整的 Shell 命令字符串
//
// 返回：
//   - string: 提取的基础命令名，如果无法识别则返回空字符串
func extractBaseCommand(command string) string {
	matches := baseCommandPattern.FindStringSubmatch(strings.TrimSpace(command))
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// isCommandWhitelisted 检查命令是否在白名单中。
// 优先检查自定义白名单，如果为空则使用默认白名单。
//
// 参数：
//   - command: 要检查的 Shell 命令
//
// 返回：
//   - bool: 如果命令在白名单中返回 true，否则返回 false
func (t *BashTool) isCommandWhitelisted(command string) bool {
	baseCmd := extractBaseCommand(command)
	if baseCmd == "" {
		return false
	}

	if len(t.customWhitelist) > 0 {
		return t.customWhitelist[baseCmd]
	}

	defaultWL := getDefaultWhitelist()
	for _, allowed := range defaultWL {
		if baseCmd == allowed {
			return true
		}
	}
	return false
}

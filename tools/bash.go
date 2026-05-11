package tools

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// Default bash command timeout in milliseconds.
const defaultBashTimeoutMs = 30000

// Maximum output size (in characters) for bash stdout/stderr.
const maxBashOutputSize = 30000

// BashTool implements a tool for executing shell commands with whitelist security.
type BashTool struct {
	whitelistEnabled     bool
	customWhitelist      map[string]bool
	sandboxConfig        *SandboxConfig
	sessionSandboxMgr    *SessionSandboxManager
}

// NewBashTool creates a Bash tool with default whitelist enabled.
func NewBashTool() core.FuncTool {
	return &BashTool{
		whitelistEnabled: true,
		customWhitelist:  make(map[string]bool),
		sandboxConfig:    DefaultSandboxConfig(),
	}
}

// NewBashToolWithWhitelist creates a Bash tool with custom whitelist.
func NewBashToolWithWhitelist(allowedCommands []string) core.FuncTool {
	wl := make(map[string]bool)
	for _, cmd := range allowedCommands {
		wl[cmd] = true
	}
	return &BashTool{
		whitelistEnabled: true,
		customWhitelist:  wl,
		sandboxConfig:    DefaultSandboxConfig(),
	}
}

// NewBashToolUnrestricted creates a Bash tool without whitelist (not recommended for production).
func NewBashToolUnrestricted() core.FuncTool {
	return &BashTool{
		whitelistEnabled: false,
		customWhitelist:  make(map[string]bool),
		sandboxConfig:    UnrestrictedSandboxConfig(),
	}
}

// NewBashToolWithSandbox creates a Bash tool with sandbox configuration.
func NewBashToolWithSandbox(config *SandboxConfig) core.FuncTool {
	return &BashTool{
		whitelistEnabled: true,
		customWhitelist:  make(map[string]bool),
		sandboxConfig:    config,
	}
}

// NewBashToolWithSessionSandbox creates a Bash tool with session-level sandbox isolation.
func NewBashToolWithSessionSandbox(mgr *SessionSandboxManager) core.FuncTool {
	return &BashTool{
		whitelistEnabled:  true,
		customWhitelist:   make(map[string]bool),
		sandboxConfig:     mgr.defaultConfig,
		sessionSandboxMgr: mgr,
	}
}

// SetSandboxConfig sets the sandbox configuration for this Bash tool.
func (t *BashTool) SetSandboxConfig(config *SandboxConfig) {
	t.sandboxConfig = config
}

// SetSessionSandboxManager sets the session-level sandbox manager.
func (t *BashTool) SetSessionSandboxManager(mgr *SessionSandboxManager) {
	t.sessionSandboxMgr = mgr
}

var baseCommandPattern = regexp.MustCompile(`^\s*([a-zA-Z][a-zA-Z0-9._\-]*)(\s|$)`)

func (t *BashTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "Bash",
		Description: "Execute shell commands and return their output. Use dedicated tools instead of bash when available.",
		Prompt: `Executes a given bash command and returns its output. The project directory persists between commands, but shell state does not.

IMPORTANT: Avoid using this tool to run cat, head, tail, sed, awk, or echo commands. Instead, use dedicated tools:
- File search: Use Glob (NOT find or ls)
- Content search: Use Grep (NOT grep or rg)
- Read files: Use Read (NOT cat/head/tail)
- Edit files: Use FileEdit (NOT sed/awk)
- Write files: Use Write (NOT echo/cat heredoc)

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
  - Before destructive operations (git reset --hard, git push --force), consider safer alternatives.`,
		Tags:          []string{"shell", "execute", "system", "command", "process"},
		SecurityLevel: core.LevelHighRisk,
		Parameters: []core.Parameter{
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
		},
	}
}

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

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", command)

	sessionID := ExtractSessionID(ctx)
	if t.sessionSandboxMgr != nil && sessionID != "" {
		cmd = t.sessionSandboxMgr.ApplyToCommand(cmd, sessionID)
	} else {
		cmd = ApplySandbox(cmd, t.sandboxConfig)
	}

	ensureTempDir(t.sandboxConfig.TempDir)

	logger.Info("executing bash command",
		"command", truncateForLog(command, 200),
		"session_id", sessionID,
		"timeout_ms", timeoutMs,
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

// truncateOutput truncates a string to maxRunes characters, appending a truncation notice if needed.
func truncateOutput(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "\n... [output truncated due to size] ..."
}

// truncateForLog truncates a string for safe logging (avoids logging huge commands).
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// dangerousPatterns defines commands that are too destructive to allow even with permission.
// This is defense-in-depth on top of the AskPermission tool.
var dangerousPatterns = []struct {
	pattern string
	reason  string
}{
	{`rm\s+-rf\s+/\s*`, "destructive: rm -rf / would erase the entire filesystem"},
	{`rm\s+-rf\s+/\*`, "destructive: rm -rf /* would erase the entire filesystem"},
	{`rm\s+-rf\s+/[a-z]*\s*$`, "destructive: recursive root-level removal is blocked"},
	{`>\s*/dev/sd[a-z]\b`, "dangerous: writing to raw disk device"},
	{`dd\s+if=.*of=/dev/sd`, "dangerous: raw disk overwrite via dd"},
	{`mkfs\.`, "dangerous: disk formatting command"},
	{`:.*\|.*:&\s*;:\s*}`, "dangerous: fork bomb detected"},
	{`(curl|wget)\s+.*\|\s*(sh|bash)`, "dangerous: remote code execution pipe (curl|sh)"},
	{`(curl|wget)\s+.*\s*>\s*/(bin|usr/bin)/`, "dangerous: remote binary download to system path"},
	{`chmod\s+-R\s+777\s+/`, "dangerous: world-writable root filesystem"},
	{`chown\s+-R.*\s+/`, "dangerous: recursive root ownership change"},
	{`shutdown\s+(now|-h|-r)`, "dangerous: system shutdown command"},
	{`reboot\b`, "dangerous: system reboot command"},
}

// detectDangerousCommand checks a shell command against known dangerous patterns.
// Returns an empty string if safe, or a block reason if matched.
func detectDangerousCommand(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, dp := range dangerousPatterns {
		if matchPattern(lower, dp.pattern) {
			return dp.reason
		}
	}
	return ""
}

// matchPattern performs a simple substring check for the given pattern.
// Uses case-insensitive matching for ASCII patterns.
func matchPattern(s, pattern string) bool {
	lowerS := strings.ToLower(s)
	lowerP := strings.ToLower(pattern)
	if len(lowerP) > len(lowerS) {
		return false
	}
	for i := 0; i <= len(lowerS)-len(lowerP); i++ {
		if lowerS[i:i+len(lowerP)] == lowerP {
			return true
		}
	}
	return false
}

// getDefaultWhitelist returns the default allowed commands for the bash tool.
func getDefaultWhitelist() []string {
	return []string{
		"ls", "cat", "head", "tail", "wc", "grep", "find",
		"echo", "printf", "pwd", "cd", "mkdir", "touch", "cp", "mv", "rm",
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
		"sed", "awk", "sort", "uniq", "cut", "tr", "tee", "xargs",
		"jq", "yq",
		"test", "[[", "true", "false", "exit", "return",
		"sleep", "wait", "bg", "fg", "jobs", "nohup", "disown",
		"basename", "dirname", "realpath", "readlink",
		"sha256sum", "md5sum", "sha1sum", "shasum",
		"openssl", "gpg", "ssh-keygen",
	}
}

// extractBaseCommand extracts the base command from a shell command string.
func extractBaseCommand(command string) string {
	matches := baseCommandPattern.FindStringSubmatch(strings.TrimSpace(command))
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// isCommandWhitelisted checks if the command's base executable is in the whitelist.
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

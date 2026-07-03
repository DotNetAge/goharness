package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/events"
)

// ---------------------------------------------------------------------------
// Platform runtime
// ---------------------------------------------------------------------------

// Platform represents the detected OS platform.
type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
	PlatformMacOS   Platform = "darwin"
)

// CurrentPlatform returns the runtime OS.
func CurrentPlatform() Platform {
	return Platform(runtime.GOOS)
}

// IsWindows, IsMacOS, IsLinux helpers.
func (p Platform) IsWindows() bool { return p == PlatformWindows }
func (p Platform) IsMacOS() bool   { return p == PlatformMacOS }
func (p Platform) IsLinux() bool   { return p == PlatformLinux }

// Shell returns the default shell executable for this platform.
func (p Platform) Shell() string {
	switch p {
	case PlatformWindows:
		return "cmd.exe"
	case PlatformMacOS:
		return "/bin/zsh"
	default:
		return "/bin/bash"
	}
}

// ScriptExtensions returns all script file extensions supported on this platform.
func (p Platform) ScriptExtensions() map[string]string {
	exts := map[string]string{
		".py":   "python",
		".sh":   "shell",
		".bash": "shell",
		".zsh":  "shell",
		".js":   "node",
		".rb":   "ruby",
		".pl":   "perl",
		".php":  "php",
	}
	if p.IsWindows() {
		exts[".bat"] = "batch"
		exts[".cmd"] = "batch"
		exts[".ps1"] = "powershell"
		exts[".vbs"] = "vbscript"
		exts[".exe"] = "executable"
	}
	if p.IsMacOS() {
		exts[".scpt"] = "applescript"
		exts[".applescript"] = "applescript"
	}
	return exts
}

// SupportedInterpreters returns interpreter names recognized on this platform.
func (p Platform) SupportedInterpreters() map[string]bool {
	interpreters := map[string]bool{
		"python": true, "python3": true, "pypy": true,
		"node": true, "nodejs": true,
		"ruby": true,
		"perl": true,
		"php":  true,
	}
	switch p {
	case PlatformWindows:
		interpreters["cmd"] = true
		interpreters["powershell"] = true
		interpreters["pwsh"] = true
		interpreters["cscript"] = true
		interpreters["wscript"] = true
	case PlatformMacOS:
		interpreters["osascript"] = true
		interpreters["bash"] = true
		interpreters["sh"] = true
		interpreters["zsh"] = true
	case PlatformLinux:
		interpreters["bash"] = true
		interpreters["sh"] = true
		interpreters["zsh"] = true
	}
	return interpreters
}

// ---------------------------------------------------------------------------
// scriptResult — internal execution result
// ---------------------------------------------------------------------------

type scriptResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Duration string `json:"duration"`
}

// ---------------------------------------------------------------------------
// scriptExecutor — internal interface for script execution strategies
// ---------------------------------------------------------------------------

type scriptExecutor interface {
	Execute(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error)
}

// ---------------------------------------------------------------------------
// platformScriptExecutor — dispatches execution based on platform + file type
// ---------------------------------------------------------------------------

type platformScriptExecutor struct {
	platform     Platform
	mu           sync.Mutex
	venvManagers map[string]*venvManager
}

func newPlatformScriptExecutor() *platformScriptExecutor {
	return &platformScriptExecutor{
		platform:     CurrentPlatform(),
		venvManagers: make(map[string]*venvManager),
	}
}

func (e *platformScriptExecutor) Execute(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	skillRoot = filepath.Clean(skillRoot)

	absSkillRoot, err := filepath.Abs(skillRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve skill root path: %w", err)
	}

	absScript, err := filepath.Abs(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve script path: %w", err)
	}

	cleanScript := filepath.Clean(absScript)
	if !strings.HasPrefix(cleanScript, absSkillRoot+string(filepath.Separator)) &&
		cleanScript != absSkillRoot {
		return nil, fmt.Errorf("script path %q is outside of skill root directory %q (path traversal blocked)", scriptPath, skillRoot)
	}

	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("script not found: %s", scriptPath)
	}

	ext := strings.ToLower(filepath.Ext(scriptPath))
	switch ext {
	case ".py":
		return e.executePython(ctx, skillRoot, scriptPath, args)
	case ".sh", ".bash", ".zsh":
		return e.executeShell(ctx, skillRoot, scriptPath, args)
	case ".rb":
		return e.executeRuby(ctx, skillRoot, scriptPath, args)
	case ".js":
		return e.executeNode(ctx, skillRoot, scriptPath, args)
	case ".bat", ".cmd":
		return e.executeBatch(ctx, skillRoot, scriptPath, args)
	case ".ps1":
		return e.executePowerShell(ctx, skillRoot, scriptPath, args)
	case ".vbs":
		return e.executeVBScript(ctx, skillRoot, scriptPath, args)
	case ".scpt", ".applescript":
		return e.executeAppleScript(ctx, skillRoot, scriptPath, args)
	case ".exe":
		return e.executeExecutable(ctx, skillRoot, scriptPath, args)
	default:
		return e.executeGeneric(ctx, skillRoot, scriptPath, args)
	}
}

func (e *platformScriptExecutor) executePython(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	key := venvKey(skillRoot)

	e.mu.Lock()
	vm, ok := e.venvManagers[key]
	if !ok {
		vm = newVenvManager(skillRoot)
		e.venvManagers[key] = vm
	}
	e.mu.Unlock()

	if err := vm.ensureVenv(ctx); err != nil {
		return nil, fmt.Errorf("failed to setup python environment: %w", err)
	}

	pythonBin := filepath.Join(vm.venvPath, "bin", "python")
	if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
		pythonBin = filepath.Join(vm.venvPath, "Scripts", "python.exe")
	}

	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{absScript}, args...)
	cmd := exec.CommandContext(ctx, pythonBin, fullArgs...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeShell(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	var cmd *exec.Cmd

	if e.platform.IsWindows() {
		bashPaths := []string{
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files (x86)\Git\bin\bash.exe`,
			`C:\Windows\System32\bash.exe`,
		}
		bashBin := ""
		for _, p := range bashPaths {
			if _, err := os.Stat(p); err == nil {
				bashBin = p
				break
			}
		}
		if bashBin == "" {
			if found, err := exec.LookPath("bash"); err == nil {
				bashBin = found
			}
		}
		if bashBin != "" {
			cmd = exec.CommandContext(ctx, bashBin, scriptPath)
			cmd.Args = append(cmd.Args, args...)
		} else {
			shell := e.platform.Shell()
			cmd = exec.CommandContext(ctx, shell, "/c", scriptPath)
			cmd.Args = append(cmd.Args, args...)
		}
	} else {
		shell := e.platform.Shell()
		cmd = exec.CommandContext(ctx, shell, scriptPath)
		cmd.Args = append(cmd.Args, args...)
	}

	cmd.Dir = skillRoot
	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeRuby(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	rubyBin := "ruby"
	if _, err := exec.LookPath("ruby"); err != nil {
		return nil, fmt.Errorf("ruby interpreter not found in PATH")
	}

	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{absScript}, args...)
	cmd := exec.CommandContext(ctx, rubyBin, fullArgs...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeNode(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	nodeBin := "node"
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node interpreter not found in PATH")
	}

	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{absScript}, args...)
	cmd := exec.CommandContext(ctx, nodeBin, fullArgs...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeBatch(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", scriptPath)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executePowerShell(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	psBin := "pwsh"
	if _, err := exec.LookPath("pwsh"); err != nil {
		psBin = "powershell"
	}

	cmd := exec.CommandContext(ctx, psBin, "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeVBScript(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	wscriptBin := "cscript"
	if _, err := exec.LookPath("cscript"); err != nil {
		wscriptBin = "wscript"
	}

	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{"//Nologo", absScript}, args...)
	cmd := exec.CommandContext(ctx, wscriptBin, fullArgs...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeAppleScript(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{absScript}, args...)
	cmd := exec.CommandContext(ctx, "osascript", fullArgs...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeExecutable(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	absScript, _ := filepath.Abs(scriptPath)
	cmd := exec.CommandContext(ctx, absScript, args...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeGeneric(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	absScript, _ := filepath.Abs(scriptPath)
	cmd := exec.CommandContext(ctx, absScript, args...)
	cmd.Dir = skillRoot

	return runScriptCommand(cmd)
}

// runScriptCommand is a shared helper that runs an exec.Cmd and captures output.
func runScriptCommand(cmd *exec.Cmd) (*scriptResult, error) {
	start := time.Now()
	stdout, err := cmd.Output()
	duration := time.Since(start).String()

	if exitErr, ok := err.(*exec.ExitError); ok {
		return &scriptResult{
			ExitCode: exitErr.ExitCode(),
			Stdout:   string(stdout),
			Stderr:   string(exitErr.Stderr),
			Duration: duration,
		}, nil
	} else if err != nil {
		return nil, err
	}

	return &scriptResult{
		ExitCode: 0,
		Stdout:   string(stdout),
		Duration: duration,
	}, nil
}

// ---------------------------------------------------------------------------
// venvManager — per-skill Python virtual environment
// ---------------------------------------------------------------------------

type venvManager struct {
	skillRoot string
	venvPath  string
	reqHash   string
	once      sync.Once
}

func newVenvManager(skillRoot string) *venvManager {
	return &venvManager{
		skillRoot: skillRoot,
		venvPath:  filepath.Join(skillRoot, ".venv"),
	}
}

func (m *venvManager) ensureVenv(ctx context.Context) error {
	var initErr error
	m.once.Do(func() {
		initErr = m.initOnce(ctx)
	})
	return initErr
}

func (m *venvManager) initOnce(ctx context.Context) error {
	if _, err := os.Stat(m.venvPath); os.IsNotExist(err) {
		if err := m.createVenv(); err != nil {
			return fmt.Errorf("create venv: %w", err)
		}
	}

	reqFile := filepath.Join(m.skillRoot, "scripts", "requirements.txt")
	if _, err := os.Stat(reqFile); os.IsNotExist(err) {
		return nil
	}

	currentHash, _ := hashFile(reqFile)
	if currentHash == m.reqHash && dirExists(m.venvPath) {
		return nil
	}

	if err := m.installRequirements(ctx, reqFile); err != nil {
		return fmt.Errorf("install requirements: %w", err)
	}
	m.reqHash = currentHash
	return nil
}

func (m *venvManager) createVenv() error {
	pythonCmd := "python3"
	if _, err := exec.LookPath("python3"); err != nil {
		pythonCmd = "python"
	}

	cmd := exec.Command(pythonCmd, "-m", "venv", m.venvPath)
	cmd.Dir = m.skillRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("venv creation failed (%s): %w", out, err)
	}
	return nil
}

func (m *venvManager) installRequirements(ctx context.Context, reqFile string) error {
	pipBin := filepath.Join(m.venvPath, "bin", "pip")
	if _, err := os.Stat(pipBin); os.IsNotExist(err) {
		pipBin = filepath.Join(m.venvPath, "Scripts", "pip.exe")
	}

	cmd := exec.CommandContext(ctx, pipBin, "install", "-r", reqFile)
	cmd.Dir = m.skillRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pip install failed (%s): %w", out, err)
	}
	return nil
}

func venvKey(dir string) string {
	h := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("%x", h)[:16]
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// ---------------------------------------------------------------------------
// RunScript — the Tool
// ---------------------------------------------------------------------------

type RunScript struct {
	info           *ToolInfo
	scriptExecutor scriptExecutor
}

func NewRunScriptTool() FuncTool {
	platform := CurrentPlatform()
	executor := newPlatformScriptExecutor()
	return &RunScript{
		info:           buildRunScriptInfo(platform),
		scriptExecutor: executor,
	}
}

func buildRunScriptInfo(platform Platform) *ToolInfo {
	description := "Execute a script file from a skill's scripts/ directory. The tool auto-detects the language from the file extension and routes to the appropriate interpreter."

	prompt := `Execute a script file, typically from an active skill's scripts/ directory. The tool auto-detects the language from the file extension and routes to the appropriate executor.

Common supported script types (all platforms):
- Python (.py) — auto-manages virtual environments and requirements.txt
- Shell (.sh, .bash, .zsh) — runs via the default platform shell
- Ruby (.rb) — runs via ruby interpreter
- Node.js (.js) — runs via node
- Perl (.pl) — runs via perl
- PHP (.php) — runs via php

Platform-specific:
- macOS also: AppleScript (.scpt, .applescript) — runs via osascript
- Windows also: Batch (.bat, .cmd), PowerShell (.ps1), VBScript (.vbs), Executables (.exe)

Usage:
- Pass the command exactly as specified in the skill's instructions.
- Include the interpreter if needed (e.g. "python scripts/analyze.py").
- The working_dir defaults to the project directory.
- Use the args parameter for additional arguments.

Notes:
- Python virtual environments are managed automatically when available.
- Output is truncated at 2KB to save context.`

	return &ToolInfo{
		Name:               "RunScript",
		Description:        description,
		Prompt:             prompt,
		MaxResultSizeChars: 30000,
		Tags:               []string{"script", "execute", "python", "shell", "skill"},
		SecurityLevel:      events.LevelSensitive,
		Parameters: []Parameter{
			{
				Name:        "command",
				Type:        "string",
				Description: "The script invocation command exactly as specified in Skill instructions. Include interpreter name if needed (e.g., 'python scripts/foo.py' or 'osascript scripts/myscript.scpt').",
				Required:    true,
			},
			{
				Name:        "working_dir",
				Type:        "string",
				Description: "Execution directory for script execution. Defaults to the project directory. Can be set to the skill's base_dir or session directory depending on context.",
				Required:    false,
			},
			{
				Name:        "args",
				Type:        "array",
				Description: "Additional arguments to pass to the script.",
				Required:    false,
			},
		},
	}
}

// Grant implements tools.PermissionRequired. RunScript is special: it
// already enforces a path-traversal block in executePlatformExecutor
// ("script path is outside of skill root directory"), but that block
// fires AFTER we've already been called. For the permission flow, we
// only want to ask the user when the script lives OUTSIDE the working
// directory (i.e. the user is being asked to bless a script that is
// not in the active project/skill root). Scripts inside the working
// directory are just like any other code execution inside the project —
// they go through normally.
//
// Hard-blocks (e.g. malformed command) are Execute-level errors, not
// Grant concerns.
func (t *RunScript) Grant(ctx context.Context, params map[string]any) (bool, string) {
	command, _ := params["command"].(string)
	if strings.TrimSpace(command) == "" {
		return true, ""
	}

	workingDir, _ := params["working_dir"].(string)
	if workingDir == "" {
		workingDir = "."
	}
	workingDir = filepath.Clean(workingDir)

	_, scriptPath := parseCommand(command, workingDir)
	if scriptPath == "" {
		return true, ""
	}

	// Resolve the script path. If it can't be made absolute (e.g. the
	// working directory is not yet realized), fall through — Execute will
	// produce a clean error.
	absScript, err := filepath.Abs(scriptPath)
	if err != nil {
		return true, ""
	}
	cleanScript := filepath.Clean(absScript)

	absWork, err := filepath.Abs(workingDir)
	if err != nil {
		return true, ""
	}
	absWork = filepath.Clean(absWork)

	// Inside the working dir? Fine.
	if cleanScript == absWork ||
		strings.HasPrefix(cleanScript, absWork+string(filepath.Separator)) {
		return true, ""
	}

	// Outside the working dir → ask the user. The actual executor will
	// still double-check, so even if Grant is bypassed we never run an
	// out-of-tree script.
	//
	// Before prompting, check the session whitelist.
	if tc := GetToolContext(ctx); tc != nil && tc.SessionWhitelist != nil {
		for _, allowed := range tc.SessionWhitelist.RunScript {
			if strings.HasPrefix(cleanScript, allowed) {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf(
		"RunScript wants to execute %q, which is outside the working directory %q. Confirm this is intended.",
		cleanScript, absWork,
	)
}

func (t *RunScript) Info() *ToolInfo {
	return t.info
}

func (t *RunScript) Execute(ctx context.Context, params map[string]any) (any, error) {
	command, ok := params["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("missing required parameter: command")
	}

	logger := getLogger(ctx)

	workingDir, _ := params["working_dir"].(string)
	if workingDir == "" {
		workingDir = "."
	}
	workingDir = filepath.Clean(workingDir)

	language, scriptPath := parseCommand(command, workingDir)
	if scriptPath == "" {
		return nil, fmt.Errorf("could not extract script path from command: %q", command)
	}

	logger.Info("executing script",
		"language", language,
		"script", scriptPath,
		"working_dir", workingDir,
	)

	var args []string
	if rawArgs, ok := params["args"].([]any); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				args = append(args, s)
			}
		}
	} else if rawArgs, ok := params["args"].([]string); ok {
		args = rawArgs
	}

	result, err := t.scriptExecutor.Execute(ctx, workingDir, scriptPath, args)
	if err != nil {
		return nil, err
	}
	return formatScriptResult(language, scriptPath, result), nil
}

// ---------------------------------------------------------------------------
// Command parsing
// ---------------------------------------------------------------------------

func parseCommand(command, baseDir string) (language, scriptPath string) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", ""
	}

	interpreters := map[string]bool{
		"python": true, "python3": true, "pypy": true,
		"node": true, "nodejs": true,
		"ruby": true,
		"perl": true,
		"php":  true,
		"bash": true, "sh": true, "zsh": true,
	}

	// Add platform-specific interpreters
	platform := CurrentPlatform()
	for k := range platform.SupportedInterpreters() {
		interpreters[k] = true
	}

	if len(parts) > 1 && interpreters[parts[0]] {
		language = parts[0]
		candidate := parts[1]
		if filepath.IsAbs(candidate) {
			scriptPath = candidate
		} else {
			scriptPath = filepath.Join(baseDir, candidate)
		}
		return
	}

	candidate := parts[0]
	if filepath.IsAbs(candidate) {
		scriptPath = candidate
	} else {
		scriptPath = filepath.Join(baseDir, candidate)
	}

	exts := platform.ScriptExtensions()
	language = exts[strings.ToLower(filepath.Ext(scriptPath))]

	return
}

// ---------------------------------------------------------------------------
// Result formatting
// ---------------------------------------------------------------------------

func formatScriptResult(language, scriptPath string, result *scriptResult) map[string]any {
	output := result.Stdout
	if output == "" {
		output = result.Stderr
	}
	if output == "" {
		output = "(no output)"
	}

	return map[string]any{
		"status":    "completed",
		"language":  language,
		"script":    filepath.Base(scriptPath),
		"exit_code": result.ExitCode,
		"output":    truncateScriptOutput(output, 2000),
		"duration":  result.Duration,
		"truncated": len(result.Stdout) > 2000 || len(result.Stderr) > 2000,
	}
}

func truncateScriptOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... [truncated by run_script tool]"
}

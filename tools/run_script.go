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
	platform      Platform
	mu            sync.Mutex
	venvManagers  map[string]*venvManager
	pythonVenvDir string // external venv override (set via RunScript.SetPythonVenv)
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
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试解析技能根目录路径 %q", skillRoot),
			WithErrDetail("无法解析技能根目录路径（通常是当前工作目录不可访问）", err),
			fmt.Sprintf("先自查：技能根目录 %q 是否真实存在且可访问？若确认无误仍失败，应停止无意义的重试并告知用户", skillRoot),
		), err)
	}

	absScript, err := filepath.Abs(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试解析脚本路径 %q", scriptPath),
			WithErrDetail("无法解析脚本路径（通常是当前工作目录不可访问）", err),
			fmt.Sprintf("先自查：脚本路径 %q 是否真实存在且可访问？若确认无误仍失败，应停止无意义的重试并告知用户", scriptPath),
		), err)
	}

	cleanScript := filepath.Clean(absScript)
	if !strings.HasPrefix(cleanScript, absSkillRoot+string(filepath.Separator)) &&
		cleanScript != absSkillRoot {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试执行脚本 %q", scriptPath),
			fmt.Sprintf("脚本路径解析为 %q，越出了技能根目录 %q 的范围（存在路径遍历风险）", cleanScript, absSkillRoot),
			fmt.Sprintf("将脚本放置到技能根目录 %s 之内，或修正脚本路径使其位于该目录范围内", absSkillRoot),
		))
	}

	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("定位脚本", scriptPath, err), err)
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
	// If an external venv is configured (via SetPythonVenv), use it directly
	// instead of auto-managing a per-skill venv. This allows consumers like
	// mindx to reuse their own Python environment.
	if e.pythonVenvDir != "" {
		pythonBin := filepath.Join(e.pythonVenvDir, "bin", "python")
		if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
			pythonBin = filepath.Join(e.pythonVenvDir, "Scripts", "python.exe")
		}
		absScript, _ := filepath.Abs(scriptPath)
		fullArgs := append([]string{absScript}, args...)
		cmd := exec.CommandContext(ctx, pythonBin, fullArgs...)
		cmd.Dir = skillRoot
		return runScriptCommand(cmd)
	}

	key := venvKey(skillRoot)

	e.mu.Lock()
	vm, ok := e.venvManagers[key]
	if !ok {
		vm = newVenvManager(skillRoot)
		e.venvManagers[key] = vm
	}
	e.mu.Unlock()

	if err := vm.ensureVenv(ctx); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建 Python 虚拟环境", vm.venvPath, err), err)
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
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试用 Ruby 解释器执行脚本 %q", scriptPath),
			"目标解释器 ruby 未安装或不在系统 PATH 中",
			"先确认系统中是否安装了 ruby（可用 Bash 执行 which ruby 或 ruby -v 确认）；若未安装，应告知用户，或改用 Bash 直接执行脚本命令",
		), err)
	}

	absScript, _ := filepath.Abs(scriptPath)
	fullArgs := append([]string{absScript}, args...)
	cmd := exec.CommandContext(ctx, rubyBin, fullArgs...)
	e.setProjectDir(ctx, cmd)

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeNode(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	nodeBin := "node"
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试用 Node.js 解释器执行脚本 %q", scriptPath),
			"目标解释器 node 未安装或不在系统 PATH 中",
			"先确认系统中是否安装了 node（可用 Bash 执行 which node 或 node -v 确认）；若未安装，应告知用户，或改用 Bash 直接执行脚本命令",
		), err)
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
	e.setProjectDir(ctx, cmd)

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executePowerShell(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	psBin := "pwsh"
	if _, err := exec.LookPath("pwsh"); err != nil {
		psBin = "powershell"
	}

	cmd := exec.CommandContext(ctx, psBin, "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.Args = append(cmd.Args, args...)
	e.setProjectDir(ctx, cmd)

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
	e.setProjectDir(ctx, cmd)

	return runScriptCommand(cmd)
}

func (e *platformScriptExecutor) executeGeneric(ctx context.Context, skillRoot, scriptPath string, args []string) (*scriptResult, error) {
	absScript, _ := filepath.Abs(scriptPath)
	cmd := exec.CommandContext(ctx, absScript, args...)
	e.setProjectDir(ctx, cmd)

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
		return nil, fmt.Errorf("%s", GuideToolFailure("RunScript", err))
	}

	return &scriptResult{
		ExitCode: 0,
		Stdout:   string(stdout),
		Duration: duration,
	}, nil
}

// setProjectDir sets the working directory to the session's ProjectDir.
// This decouples the script execution directory (where the script lives)
// from the actual working directory (where the user's project is).
func (e *platformScriptExecutor) setProjectDir(ctx context.Context, cmd *exec.Cmd) {
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		cmd.Dir = tc.Session.ProjectDir()
	}
}

// ---------------------------------------------------------------------------
// venvManager — per-skill Python virtual environment
// ---------------------------------------------------------------------------

type venvManager struct {
	skillRoot string
	venvPath  string
	reqHash   string
	mu        sync.Mutex
}

func newVenvManager(skillRoot string) *venvManager {
	return &venvManager{
		skillRoot: skillRoot,
		venvPath:  filepath.Join(skillRoot, ".venv"),
	}
}

// ensureVenv ensures the Python virtual environment exists and is up-to-date.
// Unlike the previous sync.Once-based approach, this checks the actual venv
// state on every invocation, so it correctly handles cases where the venv
// was deleted or corrupted externally after initial creation.
func (m *venvManager) ensureVenv(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Recreate venv if it was deleted or never created.
	if !dirExists(m.venvPath) {
		if err := m.createVenv(); err != nil {
			return fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建 Python 虚拟环境", m.venvPath, err), err)
		}
	}

	// Check if requirements need (re)installation.
	reqFile := filepath.Join(m.skillRoot, "scripts", "requirements.txt")
	if _, err := os.Stat(reqFile); os.IsNotExist(err) {
		return nil
	}

	currentHash, _ := hashFile(reqFile)
	if currentHash == m.reqHash {
		return nil
	}

	if err := m.installRequirements(ctx, reqFile); err != nil {
		return fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建 Python 虚拟环境", m.venvPath, err), err)
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
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建 Python 虚拟环境", m.venvPath, err), err)
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
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s（原始错误：%w）", GuideFileError("创建 Python 虚拟环境", m.venvPath, err), err)
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
	pythonVenvDir  string // external venv override (see SetPythonVenv)
}

// SetPythonVenv 设置外部 Python 虚拟环境目录。
// 设置后，executePython() 会绕过自动管理的 per-skill venv，直接使用
// 指定的 venv 中的 Python 解释器执行脚本。通常在工具初始化后、
// 注册到 ToolRegistry 之前调用。
func (t *RunScript) SetPythonVenv(venvDir string) *RunScript {
	t.pythonVenvDir = venvDir
	if exe, ok := t.scriptExecutor.(*platformScriptExecutor); ok {
		exe.pythonVenvDir = venvDir
	}
	return t
}

func NewRunScriptTool() *RunScript {
	platform := CurrentPlatform()
	executor := newPlatformScriptExecutor()
	return &RunScript{
		info:           buildRunScriptInfo(platform),
		scriptExecutor: executor,
	}
}

func buildRunScriptInfo(platform Platform) *ToolInfo {
	description := "从技能的 scripts/ 目录执行脚本文件。"

	prompt := `执行脚本文件，通常来自活动技能的 scripts/ 目录。工具根据文件扩展名自动检测语言并路由到相应的执行器。

常见支持的脚本类型（所有平台）：
- Python (.py) — 自动管理虚拟环境和 requirements.txt
- Shell (.sh, .bash, .zsh) — 通过默认平台 shell 运行
- Ruby (.rb) — 通过 ruby 解释器运行
- Node.js (.js) — 通过 node 运行
- Perl (.pl) — 通过 perl 运行
- PHP (.php) — 通过 php 运行

平台特定：
- macOS 还支持：AppleScript (.scpt, .applescript) — 通过 osascript 运行
- Windows 还支持：Batch (.bat, .cmd)、PowerShell (.ps1)、VBScript (.vbs)、可执行文件 (.exe)

用法：
- 按照技能指令中的规定准确传递命令。
- 如果需要，包含解释器（例如 "python scripts/analyze.py"）。
- working_dir 默认为项目目录。
- 使用 args 参数传递额外参数。

注意：
- Python 虚拟环境在可用时会自动管理。
- 输出在 2KB 处被截断以节省上下文。`

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
				Description: "按照技能指令中规定的脚本调用命令。如果需要，包含解释器名称（例如 'python scripts/foo.py' 或 'osascript scripts/myscript.scpt'）。",
				Required:    true,
			},
			{
				Name:        "working_dir",
				Type:        "string",
				Description: "脚本执行的执行目录。默认为项目目录。根据上下文可以设置为技能的 base_dir 或会话目录。",
				Required:    false,
			},
			{
				Name:        "args",
				Type:        "array",
				Description: "传递给脚本的额外参数。",
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
	rawCommand, ok := GetParam(params, "command")
	command := ""
	if ok {
		command, _ = rawCommand.(string)
	}
	if strings.TrimSpace(command) == "" {
		return true, ""
	}

	rawWd, ok := GetParam(params, "working_dir")
	workingDir := ""
	if ok {
		workingDir, _ = rawWd.(string)
	}
	if workingDir == "" {
		workingDir = "."
	}
	// 统一路径解析：绝对路径化 + ~ 展开 + 相对项目目录解析。
	var projectDir string
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		projectDir = tc.Session.ProjectDir()
	}
	workingDir, _ = ResolveTargetPath(workingDir, projectDir, "")

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
			if pathWithinScope(allowed, cleanScript) {
				return true, ""
			}
		}
	}
	return false, GuideRunScriptOutsideWorkspace(cleanScript, absWork)
}

func (t *RunScript) Info() *ToolInfo {
	return t.info
}

func (t *RunScript) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawCmd, ok := GetParam(params, "command")
	if !ok {
		return nil, fmt.Errorf("%s", GuideMissingParam("RunScript", "command"))
	}
	command, ok := rawCmd.(string)
	if !ok || strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("RunScript", "command"))
	}

	logger := getLogger(ctx)

	rawWd, _ := GetParam(params, "working_dir")
	workingDir, _ := rawWd.(string)
	if workingDir == "" {
		workingDir = "."
	}
	// 统一路径解析：绝对路径化 + ~ 展开 + 相对项目目录解析。
	var projectDir string
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		projectDir = tc.Session.ProjectDir()
	}
	workingDir, _ = ResolveTargetPath(workingDir, projectDir, "")

	language, scriptPath := parseCommand(command, workingDir)
	if scriptPath == "" {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试从命令 %q 中提取脚本路径，但未能解析出有效路径", command),
			"命令中不包含可识别的脚本路径（如 scripts/foo.py），或路径解析失败",
			"按技能指令中的规定重新编写命令，确保包含有效的脚本路径（如 'python scripts/foo.py'），再重试",
		))
	}

	logger.Info("executing script",
		"language", language,
		"script", scriptPath,
		"working_dir", workingDir,
	)

	var args []string
	if rawArgs, found := GetParam(params, "args"); found {
		if ra, ok := rawArgs.([]any); ok {
			for _, a := range ra {
				if s, ok := a.(string); ok {
					args = append(args, s)
				}
			}
		} else if ra, ok := rawArgs.([]string); ok {
			args = ra
		}
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
		output = "（无输出）"
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

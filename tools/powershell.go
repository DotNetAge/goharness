package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
)

const (
	powershellDefaultMaxOutputBytes = 10000
	powershellDefaultMaxDuration    = 30 * time.Second
	powershellDefaultWhitelist      = "ls,dir,cd,pwd,echo,Get-*,Set-*,Write-*,Read-*,Test-*,Out-*,Select-*,Where-*,ForEach-*,Sort-*,Group-*,Measure-*,New-*,Remove-*,Copy-*,Move-*,Rename-*,Get-ChildItem,Get-Content,Get-Service,Get-Process,Get-Item,Get-ItemProperty,Test-Path,Get-WmiObject,Get-CimInstance,Get-Command,Get-Module,Get-Help,Write-Host,Write-Output,Out-String,ConvertTo-Json,ConvertFrom-Json,Select-Object,Where-Object,ForEach-Object,Sort-Object,Group-Object,Measure-Object,New-Object,New-Item,Remove-Item,Copy-Item,Move-Item,Rename-Item,Start-Process,Stop-Process,Get-ChildItem,Get-Date,Get-Location,Set-Location,Resolve-Path,Join-Path,Split-Path"
)

var (
	robocopyExitCodes = map[int]string{
		0: "未复制任何文件。无失败。",
		1: "文件已成功复制。",
		2: "检测到额外的文件或目录。未复制任何文件。",
		3: "文件已成功复制并检测到额外文件。",
		4: "检测到不匹配的文件或目录。",
		5: "部分文件已复制。部分文件不匹配。无失败。",
		6: "存在额外文件和不匹配文件。",
		7: "文件已复制，存在文件不匹配，并且存在额外文件。",
		8: "多个文件未复制。",
	}

	findstrExitCodes = map[int]string{
		0: "在至少一个文件中找到匹配。",
		1: "未找到匹配。",
		2: "命令行语法无效。",
	}
)

// IsWindowsPlatform returns true if the current operating system is Windows.
func IsWindowsPlatform() bool {
	return runtime.GOOS == "windows"
}

// PowerShellTool implements the Windows PowerShell command execution tool.
type PowerShellTool struct {
	maxOutput   int
	maxDuration time.Duration
}

func NewPowerShellTool() FuncTool {
	return &PowerShellTool{
		maxOutput:   powershellDefaultMaxOutputBytes,
		maxDuration: powershellDefaultMaxDuration,
	}
}

func (t *PowerShellTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "PowerShell",
		MaxResultSizeChars: 30000,
		Description:        "在 Windows 上执行 PowerShell 命令。用于系统注册表查询、服务管理和 Windows 特定操作。",
		Prompt:             t.buildDescription(),
		Tags:               []string{"windows", "powershell", "system", "command"},
		SecurityLevel:      events.LevelHighRisk,
		Parameters: []Parameter{
			{
				Name:        "command",
				Type:        "string",
				Description: "要执行的 PowerShell 命令。",
				Required:    true,
			},
		},
	}
}

func (t *PowerShellTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	cmdStr, ok := params["command"].(string)
	if !ok || cmdStr == "" {
		return nil, fmt.Errorf("command 不能为空")
	}

	return t.runPowerShellCommand(ctx, cmdStr)
}

func (t *PowerShellTool) runPowerShellCommand(ctx context.Context, command string) (*PowerShellResult, error) {
	powershellPath, err := exec.LookPath("powershell.exe")
	if err != nil {
		powershellPath = "powershell.exe"
	}

	if blocked := detectDangerousPSCommand(command); blocked != "" {
		return &PowerShellResult{
			ExitCode: 126,
			Stdout:   "",
			Stderr:   fmt.Sprintf("已阻止：%s", blocked),
			Duration: time.Since(time.Now()).String(),
		}, nil
	}

	if !isPSCommandWhitelisted(command) {
		return &PowerShellResult{
			ExitCode: 126,
			Stdout:   "",
			Stderr:   fmt.Sprintf("已阻止：命令不匹配允许的 PowerShell 命令模式"),
			Duration: time.Since(time.Now()).String(),
		}, nil
	}

	args := []string{
		"-NoProfile",
		"-NonInteractive",
		"-Command", command,
	}

	maxDur := t.maxDuration
	if maxDur <= 0 {
		maxDur = powershellDefaultMaxDuration
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, maxDur)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, powershellPath, args...)

	start := time.Now()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	duration := time.Since(start).String()

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()

		stdoutStr = truncateOutput(stdoutStr, t.maxOutput)
		stderrStr = truncateOutput(stderrStr, t.maxOutput)

		stderrStr = applyPowerShellCommandSemantics(exitCode, stderrStr)

		if stderrStr != "" {
			stdoutStr = strings.TrimRight(stdoutStr, "\n") + "\n" + stderrStr
		}

		return &PowerShellResult{
			ExitCode: exitCode,
			Stdout:   stdoutStr,
			Duration: duration,
		}, nil
	} else if err != nil {
		return nil, err
	}

	return &PowerShellResult{
		ExitCode: 0,
		Stdout:   truncateOutput(stdoutStr, t.maxOutput),
		Stderr:   stderrStr,
		Duration: duration,
	}, nil
}

var dangerousPSPatterns = []struct {
	pattern string
	reason  string
}{
	{`Remove-Item\s+-Recurse`, "危险：递归删除"},
	{`Remove-Item\s+.*\*`, "危险：通配符删除"},
	{`Remove-Item\s+.*-Force`, "危险：强制删除"},
	{`Format-Volume`, "危险：磁盘格式化"},
	{`Clear-Disk`, "危险：磁盘清除"},
	{`Set-ExecutionPolicy\s+Unrestricted`, "危险：禁用执行策略"},
	{`Invoke-Expression`, "危险：任意代码执行"},
	{`Invoke-Command\s+-ComputerName`, "危险：远程命令执行"},
	{`Start-Process\s+.*-Verb\s+RunAs`, "危险：权限提升"},
	{`[System.IO.File]::`, "危险：直接 .NET I/O 绕过"},
	{`Add-Type\s+-TypeDefinition`, "危险：动态代码编译"},
	{`(New-Object\s+Net\.WebClient).*Download`, "危险：远程下载"},
	{`Stop-Computer`, "危险：系统关机"},
	{`Restart-Computer`, "危险：系统重启"},
}

func detectDangerousPSCommand(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, dp := range dangerousPSPatterns {
		if strings.Contains(lower, strings.ToLower(dp.pattern)) {
			return dp.reason
		}
	}
	return ""
}

func isPSCommandWhitelisted(command string) bool {
	firstCmd := strings.TrimSpace(strings.SplitN(command, " ", 2)[0])
	for _, allowed := range strings.Split(powershellDefaultWhitelist, ",") {
		if strings.EqualFold(firstCmd, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func applyPowerShellCommandSemantics(exitCode int, stderr string) string {
	if robocopyMsg, ok := robocopyExitCodes[exitCode]; ok {
		return robocopyMsg
	}
	if findstrMsg, ok := findstrExitCodes[exitCode]; ok {
		return findstrMsg
	}
	return stderr
}

func (t *PowerShellTool) buildDescription() string {
	b := "`"

	var sb strings.Builder

	sb.WriteString("在临时会话中以默认（非交互式）设置执行给定的 PowerShell 命令。")
	sb.WriteString("使用此工具运行与系统注册表、服务和其他 Windows 组件交互的 Windows 特定命令。")
	sb.WriteString("PowerShell 支持通过管道功能高效执行命令。")
	sb.WriteString("对于需要输入、确认或交互式访问的命令，请使用 AskUser 工具。\n\n")

	sb.WriteString("## 执行行为\n")
	sb.WriteString("- 通过 " + b + "PowerShell -Command" + b + " 运行命令\n")
	sb.WriteString("- 项目目录为当前目录\n")
	sb.WriteString("- 输出捕获 stdout 和 stderr\n")
	sb.WriteString("- 非零退出码报告为失败\n")
	sb.WriteString("- 对于破坏性命令（例如带通配符的 " + b + "Remove-Item" + b + "、" + b + "Set-ExecutionPolicy" + b + "），\n")
	sb.WriteString("  你必须先与用户确认\n")
	sb.WriteString("- 永远不要运行 " + b + "Set-ExecutionPolicy Unrestricted" + b + "\n")
	sb.WriteString("- 使用 " + b + "Start-Process" + b + " 时，添加 " + b + "-Wait" + b + " 并使用 " + b + "-RedirectStandardOutput" + b + "\n")
	sb.WriteString("  和 " + b + "-RedirectStandardError" + b + "\n")
	sb.WriteString("- 运行命令时，使用 " + b + "Out-String -Width 100" + b + " 确保输出为控制台\n")
	sb.WriteString("  正确格式化\n")
	sb.WriteString("- 使用 " + b + "Write-Host" + b + " 时，在末尾添加 " + b + "| Out-String" + b + "\n\n")

	sb.WriteString("## 输出格式化的最佳实践\n")
	sb.WriteString("- 使用 " + b + "Out-String -Width 100" + b + " 确保输出正确格式化\n")
	sb.WriteString("- 使用 " + b + "Write-Host" + b + " 时，在末尾添加 " + b + "| Out-String" + b + "\n")
	sb.WriteString("- 使用 " + b + "Select-String" + b + " 时，访问 " + b + "Line" + b + " 属性以仅提取\n")
	sb.WriteString("  匹配的文本\n")
	sb.WriteString("- 使用 " + b + "ConvertTo-Json" + b + " 时，使用 " + b + "-Compress" + b + " 以避免\n")
	sb.WriteString("  换行问题\n\n")

	sb.WriteString("## Robocopy 退出码语义\n")
	sb.WriteString("与大多数 Windows 工具不同，robocopy 使用较低的退出码表示成功，\n")
	sb.WriteString("较高的退出码表示失败。不要将 robocopy 的非零退出码视为失败：\n\n")
	sb.WriteString("| 退出码 | 含义 |\n")
	sb.WriteString("|--------|------|\n")
	for _, code := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		if msg, ok := robocopyExitCodes[code]; ok {
			sb.WriteString(fmt.Sprintf("| %d | %s |\n", code, msg))
		}
	}

	sb.WriteString("\n- 对于 robocopy，退出码 0-7 表示成功（0-3 是理想的，4-7 表示文件已复制但有额外文件）\n")
	sb.WriteString("- 对于 robocopy，退出码 8+ 表示失败\n\n")

	sb.WriteString("## findstr 退出码语义\n")
	sb.WriteString("findstr 返回与大多数命令不同的退出码：\n\n")
	sb.WriteString("| 退出码 | 含义 |\n")
	sb.WriteString("|--------|------|\n")
	for _, code := range []int{0, 1, 2} {
		if msg, ok := findstrExitCodes[code]; ok {
			sb.WriteString(fmt.Sprintf("| %d | %s |\n", code, msg))
		}
	}

	sb.WriteString("\n- 对于 findstr，退出码 0 表示成功（找到匹配）\n")
	sb.WriteString("- 对于 findstr，退出码 1 表示无匹配（不是错误）\n")
	sb.WriteString("- 对于 findstr，退出码 2 表示错误\n\n")

	sb.WriteString("## 提示工程示例\n")
	sb.WriteString("精心制作的 PowerShell 命令示例：\n")
	sb.WriteString("- " + b + "Get-Service | Out-String -Width 100" + b + "\n")
	sb.WriteString("- " + b + "Test-Path 'HKLM:\\SYSTEM\\CurrentControlSet\\Control\\ComputerName' | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-ItemProperty 'HKLM:\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion' -Name ProductName | Select-Object -ExpandProperty ProductName | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-WmiObject Win32_Processor | Select-Object Name | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-Content C:\\path\\to\\file.txt | Select-String 'pattern' | ForEach-Object { $_.Line }" + b + "\n\n")

	sb.WriteString("产生问题输出的命令示例：\n")
	sb.WriteString("- " + b + "Get-Service" + b + "（输出可能被截断）\n")
	sb.WriteString("- " + b + "Get-Content C:\\path\\to\\file.txt | Select-String 'pattern'" + b + "\n")
	sb.WriteString("- " + b + "Get-ChildItem" + b + "（输出可能很冗长）\n\n")

	sb.WriteString("## 重要提示\n")
	sb.WriteString("- 在 Windows 上使用此工具执行快速命令和脚本\n")
	sb.WriteString("- 对于长时间运行的任务，考虑使用 " + b + "Crontab" + b + " 代替\n")
	sb.WriteString("- 对于需要用户输入的任务，请使用 AskUser 工具")

	return sb.String()
}

// PowerShellResult is the result returned by PowerShell command execution.
type PowerShellResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Duration string `json:"duration"`
}

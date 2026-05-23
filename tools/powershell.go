package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/core"
)

const (
	powershellDefaultMaxOutputBytes = 10000
	powershellDefaultMaxDuration    = time.Duration(0)
)

var (
	robocopyExitCodes = map[int]string{
		0: "No files were copied. No failure.",
		1: "Files were copied successfully.",
		2: "Extra files or directories were detected. No files were copied.",
		3: "Files were copied successfully and extra files were detected.",
		4: "Mismatched files or directories were detected.",
		5: "Some files were copied. Some files were mismatched. No failure.",
		6: "Additional files and mismatched files exist.",
		7: "Files were copied, a file mismatch was present, and additional files were present.",
		8: "Several files didn't copy.",
	}

	findstrExitCodes = map[int]string{
		0: "A match was found in at least one file.",
		1: "A match was not found.",
		2: "Invalid command-line syntax.",
	}
)

// IsWindowsPlatform returns true if the current operating system is Windows.
func IsWindowsPlatform() bool {
	return runtime.GOOS == "windows"
}

// PowerShellTool implements the Windows PowerShell command execution tool.
type PowerShellTool struct {
	maxOutput         int
	maxDuration       time.Duration
}

func NewPowerShellTool() core.FuncTool {
	return &PowerShellTool{
		maxOutput:     powershellDefaultMaxOutputBytes,
		maxDuration:   powershellDefaultMaxDuration,
	}
}

func (t *PowerShellTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        "PowerShell",
		MaxResultSizeChars: 30000,
		Description: "Execute PowerShell commands on Windows. Use for system registry queries, service management, and Windows-specific operations.",
		Prompt:      t.buildDescription(),
		Tags:        []string{"windows", "powershell", "system", "command"},
		SecurityLevel: core.LevelHighRisk,
		Parameters: []core.Parameter{
			{
				Name:        "command",
				Type:        "string",
				Description: "The PowerShell command to execute.",
				Required:    true,
			},
		},
	}
}

func (t *PowerShellTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	cmdStr, ok := params["command"].(string)
	if !ok || cmdStr == "" {
		return nil, fmt.Errorf("command is required")
	}

	return t.runPowerShellCommand(ctx, cmdStr)
}

func (t *PowerShellTool) runPowerShellCommand(ctx context.Context, command string) (*PowerShellResult, error) {
	powershellPath, err := exec.LookPath("powershell.exe")
	if err != nil {
		powershellPath = "powershell.exe"
	}

	args := []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", command,
	}

	var cmd *exec.Cmd
	if t.maxDuration > 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, t.maxDuration)
		defer cancel()
		cmd = exec.CommandContext(timeoutCtx, powershellPath, args...)
	} else {
		cmd = exec.CommandContext(ctx, powershellPath, args...)
	}



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

	sb.WriteString("Executes a given PowerShell command in a temporary session with default (non-interactive) settings. ")
	sb.WriteString("Use this tool to run Windows-specific commands that interact with the system registry, services, ")
	sb.WriteString("and other Windows components. PowerShell enables efficient execution of commands with pipelining ")
	sb.WriteString("capabilities. For commands that require input, confirmation, or interactive access, use the AskUser tool.\n\n")

	sb.WriteString("## Execution behavior\n")
	sb.WriteString("- Commands run via " + b + "PowerShell -Command" + b + "\n")
	sb.WriteString("- Project directory is the current directory\n")
	sb.WriteString("- Output captures stdout and stderr\n")
	sb.WriteString("- Non-zero exit codes are reported as failures\n")
	sb.WriteString("- For destructive commands (e.g., " + b + "Remove-Item" + b + " with wildcard, " + b + "Set-ExecutionPolicy" + b + "),\n")
	sb.WriteString("  you MUST confirm with the user first\n")
	sb.WriteString("- Never run " + b + "Set-ExecutionPolicy Unrestricted" + b + "\n")
	sb.WriteString("- When using " + b + "Start-Process" + b + ", add " + b + "-Wait" + b + " and use " + b + "-RedirectStandardOutput" + b + "\n")
	sb.WriteString("  and " + b + "-RedirectStandardError" + b + "\n")
	sb.WriteString("- When running commands, use " + b + "Out-String -Width 100" + b + " to ensure the output is properly\n")
	sb.WriteString("  formatted for the console\n")
	sb.WriteString("- When using " + b + "Write-Host" + b + ", add " + b + "| Out-String" + b + " at the end\n\n")

	sb.WriteString("## Best practices for output formatting\n")
	sb.WriteString("- Use " + b + "Out-String -Width 100" + b + " to ensure the output is properly formatted\n")
	sb.WriteString("- When using " + b + "Write-Host" + b + ", add " + b + "| Out-String" + b + " at the end\n")
	sb.WriteString("- When using " + b + "Select-String" + b + ", access the " + b + "Line" + b + " property to extract\n")
	sb.WriteString("  just the matching text\n")
	sb.WriteString("- When using " + b + "ConvertTo-Json" + b + ", use " + b + "-Compress" + b + " to avoid\n")
	sb.WriteString("  newline issues\n\n")

	sb.WriteString("## Robocopy exit code semantics\n")
	sb.WriteString("Unlike most Windows tools, robocopy uses LOW exit codes to indicate success and HIGH\n")
	sb.WriteString("exit codes to indicate failures. Do NOT treat non-zero exit codes as failures for robocopy:\n\n")
	sb.WriteString("| Exit Code | Meaning |\n")
	sb.WriteString("|-----------|---------|\n")
	for _, code := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8} {
		if msg, ok := robocopyExitCodes[code]; ok {
			sb.WriteString(fmt.Sprintf("| %d | %s |\n", code, msg))
		}
	}

	sb.WriteString("\n- For robocopy, exit codes 0-7 indicate SUCCESS (0-3 are ideal, 4-7 mean files copied with extras)\n")
	sb.WriteString("- For robocopy, exit codes 8+ indicate FAILURE\n\n")

	sb.WriteString("## findstr exit code semantics\n")
	sb.WriteString("findstr returns different exit codes than most commands:\n\n")
	sb.WriteString("| Exit Code | Meaning |\n")
	sb.WriteString("|-----------|---------|\n")
	for _, code := range []int{0, 1, 2} {
		if msg, ok := findstrExitCodes[code]; ok {
			sb.WriteString(fmt.Sprintf("| %d | %s |\n", code, msg))
		}
	}

	sb.WriteString("\n- For findstr, exit code 0 means SUCCESS (match found)\n")
	sb.WriteString("- For findstr, exit code 1 means NO MATCH (not an error)\n")
	sb.WriteString("- For findstr, exit code 2 means ERROR\n\n")

	sb.WriteString("## Prompt engineering examples\n")
	sb.WriteString("Examples of well-crafted PowerShell commands:\n")
	sb.WriteString("- " + b + "Get-Service | Out-String -Width 100" + b + "\n")
	sb.WriteString("- " + b + "Test-Path 'HKLM:\\SYSTEM\\CurrentControlSet\\Control\\ComputerName' | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-ItemProperty 'HKLM:\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion' -Name ProductName | Select-Object -ExpandProperty ProductName | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-WmiObject Win32_Processor | Select-Object Name | Out-String" + b + "\n")
	sb.WriteString("- " + b + "Get-Content C:\\path\\to\\file.txt | Select-String 'pattern' | ForEach-Object { $_.Line }" + b + "\n\n")

	sb.WriteString("Examples of commands that produce problematic output:\n")
	sb.WriteString("- " + b + "Get-Service" + b + " (output may be truncated)\n")
	sb.WriteString("- " + b + "Get-Content C:\\path\\to\\file.txt | Select-String 'pattern'" + b + "\n")
	sb.WriteString("- " + b + "Get-ChildItem" + b + " (output may be verbose)\n\n")

	sb.WriteString("## Important notes\n")
	sb.WriteString("- Use this tool for quick commands and scripts on Windows\n")
	sb.WriteString("- For long-running tasks, consider using " + b + "Crontab" + b + " instead\n")
	sb.WriteString("- For tasks requiring user input, use the AskUser tool")

	return sb.String()
}

// PowerShellResult is the result returned by PowerShell command execution.
type PowerShellResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Duration string `json:"duration"`
}

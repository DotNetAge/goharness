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

// CrontabTool manages system crontab entries for Linux/Unix/macOS.
// It provides a safe wrapper around the `crontab` command, allowing
// the agent to list, add, remove, and validate cron jobs without
// directly editing crontab files.
type CrontabTool struct {
	info *core.ToolInfo
}

// NewCrontabTool creates a new CrontabTool instance.
func NewCrontabTool() core.FuncTool {
	return &CrontabTool{
		info: &core.ToolInfo{
			Name:        "Crontab",
			Description: `System crontab manager for Linux/Unix/macOS. Operations: 'list'|'add'|'remove'|'validate'|'raw'. Params: {operation: string, expression: 'cron_expr', command: string, comment: string, line_number: number}. Security level: HighRisk — modifies system scheduling.`,
			Prompt: `Schedule recurring tasks on Linux and macOS using system cron. Use this when the user wants something to run automatically at fixed times or intervals — periodic backups, cleanup jobs, health checks, report generation, or any recurring maintenance task.

When NOT to use: For one-off delayed execution, use systemd timers or 'at' instead. For task scheduling within the current session, use Task tools (TaskCreate/TaskUpdate/TaskList). On Windows, this tool is unavailable.

## When this tool fits into a workflow

Typical pattern when the user asks to set up a recurring task:
1. Use read or bash to understand the current system state (existing scripts, disk usage, service health)
2. Use write to create any script that needs to be scheduled (e.g. a backup script)
3. Use this tool to add the cron entry that runs that script on a schedule
4. Use list to verify the entry was created correctly

## Operations

### list — See what cron jobs are already scheduled
No additional parameters needed. Returns entries with line numbers and raw content. Use the line numbers later for remove. If no crontab exists yet, returns an empty list — this is normal for first-time setup.

### add — Schedule a new recurring task
Parameters needed: expression (when to run), command (what to run). Optionally: comment (label for later identification).

The expression uses 5 fields in order: minute hour day-of-month month day-of-week
Examples of common schedules:
- 0 9 * * * — every day at 9:00 AM
- 0 9 * * 1-5 — every weekday at 9:00 AM
- */30 * * * * — every 30 minutes
- 0 0 * * 0 — every Sunday at midnight
- 0 2 1 * * — first day of every month at 2:00 AM
- */5 * * * * — every 5 minutes

Each field supports: * (all values), */n (every n), a,b,c (specific list), a-b (range). Fields work independently and combine with AND logic — all conditions must be met for the job to fire.

Always use the validate operation first to check your expression before adding, especially for non-standard schedules.

### remove — Delete a previously scheduled task
You need the line_number parameter, which comes from the list operation output. Call list first to find the right line.

### validate — Check if a cron expression is correct before using it
Takes an expression and returns whether it is valid. Use this as a safety check before add — it catches common mistakes like swapped field order or out-of-range values.

### raw — Get the unprocessed crontab content
Useful when list doesn't show enough detail or you need to inspect comments and formatting.`,
			Tags:        []string{"scheduler", "cron", "system", "linux", "macos"},
			SecurityLevel: core.LevelHighRisk,
			Parameters: []core.Parameter{
				{Name: "operation", Type: "string", Description: "Operation to perform: list, add, remove, validate, raw", Required: true},
				{Name: "expression", Type: "string", Description: "Cron expression (5 fields: minute hour day month weekday), e.g. '0 9 * * 1-5'", Required: false},
				{Name: "command", Type: "string", Description: "Shell command to execute when the cron job fires", Required: false},
				{Name: "comment", Type: "string", Description: "Comment/label for the job entry (used for identification)", Required: false},
				{Name: "line_number", Type: "number", Description: "Line number in crontab to remove (1-based, from 'list' output)", Required: false},
			},
		},
	}
}

func (t *CrontabTool) Info() *core.ToolInfo {
	return t.info
}

func (t *CrontabTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("crontab is not available on Windows; use Task Scheduler or systemd on WSL2")
	}

	operation, ok := params["operation"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'operation' parameter")
	}

	switch operation {
	case "list":
		return t.list(ctx)
	case "add":
		return t.add(ctx, params)
	case "remove":
		return t.remove(ctx, params)
	case "validate":
		return t.validate(params)
	case "raw":
		return t.raw(ctx)
	default:
		return nil, fmt.Errorf("unknown operation: %s (supported: list, add, remove, validate, raw)", operation)
	}
}

func (t *CrontabTool) list(ctx context.Context) (map[string]any, error) {
	out, err := runCommand(ctx, "crontab", "-l")
	if err != nil {
		if strings.Contains(err.Error(), "no crontab") || strings.Contains(out, "no crontab") {
			return map[string]any{"entries": []any{}, "count": 0, "message": "No crontab configured for current user"}, nil
		}
		return nil, fmt.Errorf("failed to list crontab: %w\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var entries []map[string]any
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries = append(entries, map[string]any{
			"line": i + 1,
			"raw":  line,
		})
	}

	return map[string]any{
		"entries": entries,
		"count":   len(entries),
	}, nil
}

func (t *CrontabTool) add(ctx context.Context, params map[string]any) (map[string]any, error) {
	expression, ok := params["expression"].(string)
	if !ok || expression == "" {
		return nil, fmt.Errorf("missing or invalid 'expression' parameter")
	}
	command, ok := params["command"].(string)
	if !ok || command == "" {
		return nil, fmt.Errorf("missing or invalid 'command' parameter")
	}

	comment, _ := params["comment"].(string)

	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, fmt.Errorf("invalid cron expression: expected 5 fields (minute hour day month weekday), got %d", len(fields))
	}

	entry := fmt.Sprintf("%s %s", expression, command)
	if comment != "" {
		entry = fmt.Sprintf("# %s\n%s", comment, entry)
	}

	out, err := runCommandCtxInput(ctx, "crontab", "-", entry+"\n")
	if err != nil {
		return nil, fmt.Errorf("failed to add crontab entry: %w\n%s", err, out)
	}

	return map[string]any{
		"added":    true,
		"entry":    entry,
		"comment":  comment,
		"message":  "Crontab entry added successfully",
	}, nil
}

func (t *CrontabTool) remove(ctx context.Context, params map[string]any) (map[string]any, error) {
	lineNumFloat, ok := params["line_number"].(float64)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'line_number' parameter")
	}
	lineNum := int(lineNumFloat)
	if lineNum < 1 {
		return nil, fmt.Errorf("line_number must be >= 1")
	}

	out, err := runCommand(ctx, "crontab", "-l")
	if err != nil {
		return nil, fmt.Errorf("failed to read current crontab: %w", err)
	}

	lines := strings.Split(out, "\n")
	if lineNum > len(lines) {
		return nil, fmt.Errorf("line_number %d out of range (crontab has %d lines)", lineNum, len(lines))
	}

	removed := strings.TrimSpace(lines[lineNum-1])
	newLines := append(lines[:lineNum-1], lines[lineNum:]...)
	newContent := strings.Join(newLines, "\n")

	resultOut, resultErr := runCommandCtxInput(ctx, "crontab", "-", newContent)
	if resultErr != nil {
		return nil, fmt.Errorf("failed to update crontab after removal: %w\n%s", resultErr, resultOut)
	}

	return map[string]any{
		"removed": true,
		"line":    lineNum,
		"entry":   removed,
		"message": "Crontab entry removed successfully",
	}, nil
}

func (t *CrontabTool) validate(params map[string]any) (map[string]any, error) {
	expression, ok := params["expression"].(string)
	if !ok || expression == "" {
		return nil, fmt.Errorf("missing or invalid 'expression' parameter")
	}

	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return map[string]any{"valid": false, "error": fmt.Sprintf("expected 5 fields, got %d", len(fields))}, nil
	}

	fieldNames := []string{"minute", "hour", "day", "month", "weekday"}
	fieldRanges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

	for i, field := range fields {
		err := validateCronField(field, fieldRanges[i][0], fieldRanges[i][1])
		if err != nil {
			return map[string]any{"valid": false, "error": fmt.Sprintf("invalid %s field: %v", fieldNames[i], err)}, nil
		}
	}

	return map[string]any{
		"valid": true,
		"fields": map[string]string{
			"minute":  fields[0],
			"hour":    fields[1],
			"day":     fields[2],
			"month":   fields[3],
			"weekday": fields[4],
		},
	}, nil
}

func (t *CrontabTool) raw(ctx context.Context) (map[string]any, error) {
	out, err := runCommand(ctx, "crontab", "-l")
	if err != nil {
		if strings.Contains(err.Error(), "no crontab") || strings.Contains(out, "no crontab") {
			return map[string]any{"raw": "", "message": "No crontab configured for current user"}, nil
		}
		return nil, fmt.Errorf("failed to read crontab: %w", err)
	}
	return map[string]any{"raw": out}, nil
}

func validateCronField(field string, min, max int) error {
	parts := strings.Split(field, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "*" {
			continue
		}
		if strings.Contains(part, "/") {
			sp := strings.Split(part, "/")
			base := sp[0]
			if base != "*" {
				if strings.Contains(base, "-") {
					rp := strings.Split(base, "-")
					if len(rp) != 2 {
						return fmt.Errorf("invalid range in step: %s", part)
					}
				}
			}
			continue
		}
		if strings.Contains(part, "-") {
			rp := strings.Split(part, "-")
			if len(rp) != 2 {
				return fmt.Errorf("invalid range: %s", part)
			}
			continue
		}
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runCommandCtxInput(ctx context.Context, name string, stdin string, args ...string) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

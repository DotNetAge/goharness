package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DotNetAge/goreact/events"
	"github.com/google/uuid"
)

func NewToolExecutor(registry ToolRegistry, opts ...ExecutorOption) ToolExecutor {
	cfg := &executorConfig{registry: registry}
	for _, opt := range opts {
		opt(cfg)
	}
	return &implToolExecutor{cfg: cfg}
}

type implToolExecutor struct {
	cfg *executorConfig
}

func (e *implToolExecutor) ResetCycle() {}

func (e *implToolExecutor) Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error) {
	tool, ok := e.cfg.registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}

	toolInfo := tool.Info()
	useCtx := &ToolUseContext{
		ToolName: name,
		ToolInfo: toolInfo,
		Params:   params,
		Ctx:      ctx,
	}

	if e.cfg.permissionChecker != nil {
		permResult := e.cfg.permissionChecker.CheckPermissions(useCtx)
		if e.cfg.logger != nil {
			e.cfg.logger.Debug("[executor] permission check", "tool", name, "behavior", permResult.Behavior, "msg", permResult.Message)
		}
		switch permResult.Behavior {
		case PermissionDeny:
			if e.cfg.eventEmitter != nil {
				e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionDenied, permResult.Message))
			}
			if e.cfg.logger != nil {
				e.cfg.logger.Info("[executor] tool denied", "tool", name, "reason", permResult.Message)
			}
			return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied: %s", name, permResult.Message)}, nil

		case PermissionAsk:
			if e.cfg.logger != nil {
				e.cfg.logger.Info("[executor] awaiting user response", "tool", name)
			}
			result := e.awaitUserResponse(name, params, toolInfo.SecurityLevel, useCtx)
			switch result.Behavior {
			case PermissionDeny:
				if e.cfg.eventEmitter != nil {
					e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionDenied, result.Message))
				}
				return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied by user: %s", name, result.Message)}, nil
			case PermissionAllow:
				if result.UpdatedInput != nil {
					params = result.UpdatedInput
					useCtx.Params = params
				}
			}
		case PermissionAllow:
			if permResult.UpdatedInput != nil {
				params = permResult.UpdatedInput
				useCtx.Params = params
			}
		}
	}

	if e.cfg.logger != nil {
		e.cfg.logger.Debug("[executor] executing tool", "tool", name, "params", params)
	}

	toolCtx := &ToolContext{
		EmitEvent:   e.cfg.eventEmitter,
		ResultStore: e.cfg.resultStore,
		KVStore:     e.cfg.kvStore,
		FileStore:   e.cfg.fileStore,
		SessionID:   e.cfg.sessionID,
		Logger:      e.cfg.logger,
		ProjectDir:  e.cfg.projectDir,
		SessionDir:  e.cfg.sessionDir,
	}
	execCtx := WithToolContext(ctx, toolCtx)

	start := time.Now()
	result, err := tool.Execute(execCtx, params)
	duration := time.Since(start)

	if e.cfg.logger != nil {
		if err != nil {
			e.cfg.logger.Debug("[executor] tool execution error", "tool", name, "duration_ms", duration.Milliseconds(), "error", err.Error())
		} else {
			e.cfg.logger.Debug("[executor] tool execution completed", "tool", name, "duration_ms", duration.Milliseconds())
		}
	}

	if err != nil {
		enhanced := enhanceFileError(err, extractPath(params))
		return &ToolExecutionResult{ToolName: name, Duration: duration, Error: enhanced}, nil
	}

	str, ok := result.(string)
	if !ok {
		b, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			str = fmt.Sprintf("%v", result)
		} else {
			str = string(b)
		}
	}

	str = e.processResult(name, str, toolInfo)

	return &ToolExecutionResult{
		Result:   str,
		Duration: duration,
		ToolName: name,
	}, nil
}

func (e *implToolExecutor) awaitUserResponse(name string, params map[string]any, secLevel events.SecurityLevel, useCtx *ToolUseContext) PermissionResult {
	ch := make(chan PermissionResult, 1)

	if name == "AskUser" {
		questions := extractAskUserQuestions(params)
		if e.cfg.eventEmitter != nil {
			e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.AskUserRequest,
				events.NewAskUserRequestData(uuid.New().String(), questions, func(answers map[string]string) {
					ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: map[string]any{"answers": answers}}
				}),
			))
		}
		select {
		case result := <-ch:
			return result
		case <-useCtx.Ctx.Done():
			return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
		}
	}

	if e.cfg.eventEmitter != nil {
		e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionRequest,
			events.NewPermissionRequestData(
				uuid.New().String(),
				name,
				params,
				"This tool requires your approval before execution.",
				secLevel,
				func(updatedInput map[string]any) {
					ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: updatedInput}
				},
				func(reason string) {
					ch <- PermissionResult{Behavior: PermissionDeny, Message: reason}
				},
			),
		))
	}
	select {
	case result := <-ch:
		return result
	case <-useCtx.Ctx.Done():
		return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
	}
}

func extractAskUserQuestions(params map[string]any) []events.AskUserQuestion {
	question, _ := params["question"].(string)
	if question == "" {
		return nil
	}
	multi, _ := params["multiSelect"].(bool)
	q := events.AskUserQuestion{
		Question:    question,
		MultiSelect: multi,
	}
	if opts, ok := params["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				q.Options = append(q.Options, s)
			}
		}
	}
	return []events.AskUserQuestion{q}
}

func enhanceFileError(err error, path string) error {
	if path == "" || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	suggestions := findSimilarPaths(dir, filepath.Base(path))
	if len(suggestions) == 0 {
		return fmt.Errorf("%w\nFile not found: %s", err, path)
	}
	return fmt.Errorf("%w\nFile not found: %s\n\nDid you mean one of these?\n%s",
		err, path, strings.Join(suggestions, "\n"))
}

func extractPath(params map[string]any) string {
	for _, key := range []string{"path", "file_path", "filepath", "dir", "directory"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func findSimilarPaths(dir, base string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var suggestions []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(strings.ToLower(name), strings.ToLower(base)) ||
			strings.Contains(strings.ToLower(base), strings.ToLower(name)) {
			suggestions = append(suggestions, filepath.Join(dir, name))
			if len(suggestions) >= 3 {
				break
			}
		}
	}
	return suggestions
}

func (e *implToolExecutor) processResult(toolName, str string, toolInfo *ToolInfo) string {
	if toolInfo.MaxResultSizeChars == -1 {
		return str
	}
	threshold := 25000
	if toolInfo.MaxResultSizeChars > 0 {
		threshold = toolInfo.MaxResultSizeChars
	}
	runes := []rune(str)
	if len(runes) <= threshold {
		return str
	}
	return string(runes[:threshold]) + "\n... [truncated: result exceeds size limit] ..."
}

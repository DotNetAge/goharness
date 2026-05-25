package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ToolExecutionResult struct {
	Result      string
	Metadata    any    // structured data for system consumers (UI, hooks, logging), not sent to LLM
	Duration    time.Duration
	Error       error
	ToolName    string
}

type ToolExecutor interface {
	Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error)
	ResetCycle()
}

type toolExecutorConfig struct {
	registry          ToolRegistry
	permissionChecker ToolPermissionChecker
	resultLimits      ToolResultLimits
	eventEmitter      func(ReactEvent)
	resultStore       *ResultStore
	kvStore           KVStore
	fileStore         FileStore
	sessionID         string
	logger            Logger // Unified logging interface

	// Directory context (Design-time safety: guaranteed by Agent layer)
	projectDir string // Layer 2: Set via WithProjectDirExecutor() or Agent's WithProjectDir()
	sessionDir string // Layer 3: Set via WithSessionDirExecutor() or auto-resolved from SessionStore
}

type ExecutorOption func(*toolExecutorConfig)

func WithPermissionChecker(checker ToolPermissionChecker) ExecutorOption {
	return func(c *toolExecutorConfig) { c.permissionChecker = checker }
}

func WithLogger(logger Logger) ExecutorOption {
	return func(c *toolExecutorConfig) { c.logger = logger }
}

func WithResultLimits(limits ToolResultLimits) ExecutorOption {
	return func(c *toolExecutorConfig) { c.resultLimits = limits }
}

func WithEventEmitter(emitter func(ReactEvent)) ExecutorOption {
	return func(c *toolExecutorConfig) { c.eventEmitter = emitter }
}

func WithResultStore(store *ResultStore) ExecutorOption {
	return func(c *toolExecutorConfig) { c.resultStore = store }
}

func WithKVStore(store KVStore) ExecutorOption {
	return func(c *toolExecutorConfig) { c.kvStore = store }
}

func WithFileStore(store FileStore) ExecutorOption {
	return func(c *toolExecutorConfig) { c.fileStore = store }
}

func WithSessionID(id string) ExecutorOption {
	return func(c *toolExecutorConfig) { c.sessionID = id }
}

// WithProjectDirExecutor sets the project working directory for tool execution.
// This is typically called by the Agent layer to inject its configured ProjectDir.
//
// Design-time safety:
//   - When set, all tools (edit/read/write) will use this as their base directory
//   - LLM receives this directory in its Environment section of system prompt
//   - Prevents "file not found" errors caused by ambiguous working directory
func WithProjectDirExecutor(dir string) ExecutorOption {
	return func(c *toolExecutorConfig) { c.projectDir = dir }
}

// WithSessionDirExecutor sets the session sandbox directory for tool execution.
// This is typically auto-resolved from SessionStore when available.
//
// When set:
//   - Tools can isolate session-specific files (temp files, drafts, etc.)
//   - LLM knows where to place session-scoped output
func WithSessionDirExecutor(dir string) ExecutorOption {
	return func(c *toolExecutorConfig) { c.sessionDir = dir }
}

func NewToolExecutor(registry ToolRegistry, opts ...ExecutorOption) ToolExecutor {
	cfg := &toolExecutorConfig{registry: registry}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.resultLimits.MaxResultSizeChars == 0 {
		cfg.resultLimits.MaxResultSizeChars = DefaultToolResultLimits().MaxResultSizeChars
	}
	if cfg.resultLimits.MaxToolResultsPerMessageChars == 0 {
		cfg.resultLimits.MaxToolResultsPerMessageChars = DefaultToolResultLimits().MaxToolResultsPerMessageChars
	}
	return &defaultToolExecutor{
		cfg: cfg,
	}
}

type defaultToolExecutor struct {
	cfg *toolExecutorConfig
}

func (e *defaultToolExecutor) ResetCycle() {
	// ResetCycle is a no-op after removing per-executor charsUsed tracking.
	// Budget enforcement is handled by ToolResultBudgetEnforcer at the reactor layer.
}

func (e *defaultToolExecutor) Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error) {
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
				e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionDenied, permResult.Message))
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
					e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionDenied, result.Message))
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

	// Inject ToolContext so bridge tools (subagent, etc.) can access event bus
	// Directory context is guaranteed by Agent layer (Design-time safety)
	toolCtx := &ToolContext{
		EmitEvent:   e.cfg.eventEmitter,
		ResultStore: e.cfg.resultStore,
		KVStore:     e.cfg.kvStore,
		FileStore:   e.cfg.fileStore,
		SessionID:   e.cfg.sessionID,
		Logger:      e.cfg.logger,
		ProjectDir:  e.cfg.projectDir, // Layer 2: Always set (defaults to os.Getwd() in Agent)
		SessionDir:  e.cfg.sessionDir, // Layer 3: Set when SessionStore is available
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

// awaitUserResponse emits an event and blocks until the user responds.
// For AskUser tools, emits AskUserRequest (Reply callback).
// For all other tools, emits PermissionRequest (Grant/Deny callbacks).
func (e *defaultToolExecutor) awaitUserResponse(name string, params map[string]any, secLevel SecurityLevel, useCtx *ToolUseContext) PermissionResult {
	ch := make(chan PermissionResult, 1)

	if name == "AskUser" {
		questions := extractAskUserQuestions(params)
		if e.cfg.eventEmitter != nil {
			e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", AskUserRequest, AskUserRequestData{
				TickID:    uuid.New().String(),
				Questions: questions,
				reply: func(answers map[string]string) {
					ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: map[string]any{"answers": answers}}
				},
			}))
		}
		select {
		case result := <-ch:
			return result
		case <-useCtx.Ctx.Done():
			return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
		}
	}

	if e.cfg.eventEmitter != nil {
		e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionRequest, PermissionRequestData{
			TickID:        uuid.New().String(),
			ToolName:      name,
			Params:        params,
			Reason:        "This tool requires your approval before execution.",
			SecurityLevel: secLevel,
			grant: func(updatedInput map[string]any) {
				ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: updatedInput}
			},
			deny: func(reason string) {
				ch <- PermissionResult{Behavior: PermissionDeny, Message: reason}
			},
		}))
	}
	select {
	case result := <-ch:
		return result
	case <-useCtx.Ctx.Done():
		return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
	}
}

// extractAskUserQuestions converts tool params into AskUserQuestion slice.
func extractAskUserQuestions(params map[string]any) []AskUserQuestion {
	question, _ := params["question"].(string)
	if question == "" {
		return nil
	}
	multi, _ := params["multiSelect"].(bool)
	q := AskUserQuestion{
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
	return []AskUserQuestion{q}
}

// enhanceFileError wraps file-not-found errors with similar path suggestions.
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

// extractPath attempts to extract a "path" or "file_path" parameter from the params map.
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

// findSimilarPaths scans the directory for entries with similar names.
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

func (e *defaultToolExecutor) processResult(toolName, str string, toolInfo *ToolInfo) string {
	if toolInfo.MaxResultSizeChars == -1 {
		return str
	}

	// Simplified: only check per-tool threshold.
	// The actual persistence and aggregate budget enforcement
	// is handled by ToolResultBudgetEnforcer in the reactor layer.
	threshold := e.cfg.resultLimits.MaxResultSizeChars
	if toolInfo.MaxResultSizeChars > 0 {
		threshold = toolInfo.MaxResultSizeChars
	}
	charCount := len([]rune(str))
	if charCount <= threshold {
		return str
	}

	// Mark as needing reactor-level processing by returning unchanged.
	// The callers (e.g., persistStep in reactor) will apply budget enforcement.
	return str
}

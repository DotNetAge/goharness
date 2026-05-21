package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type ToolExecutionResult struct {
	Result      string
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
		switch permResult.Behavior {
		case PermissionDeny:
			if e.cfg.eventEmitter != nil {
				e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionDenied, permResult.Message))
			}
			return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied: %s", name, permResult.Message)}, nil
		case PermissionAsk:
			if e.cfg.eventEmitter != nil {
				e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionRequest, PermissionRequestData{
					ToolName:      name,
					Params:        params,
					Reason:        permResult.Message,
					SecurityLevel: toolInfo.SecurityLevel,
					Questions:     permResult.Questions,
				}))
			}
			if responder, ok := e.cfg.permissionChecker.(PermissionResponder); ok {
				finalResult := responder.BlockAndWait(useCtx)
				switch finalResult.Behavior {
				case PermissionDeny:
					if e.cfg.eventEmitter != nil {
						e.cfg.eventEmitter(NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", PermissionDenied, finalResult.Message))
					}
					return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied by user: %s", name, finalResult.Message)}, nil
				case PermissionAllow:
					if finalResult.UpdatedInput != nil {
						params = finalResult.UpdatedInput
						useCtx.Params = params
					}
				}
			}
		case PermissionAllow:
			if permResult.UpdatedInput != nil {
				params = permResult.UpdatedInput
				useCtx.Params = params
			}
		}
	}

	// Inject ToolContext so bridge tools (delegate, etc.) can access event bus
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

	if err != nil {
		return &ToolExecutionResult{ToolName: name, Duration: duration, Error: err}, nil
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

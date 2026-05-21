package reactor

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/tools"
)

const (
	// StreamChannelBufferSize is the buffer size for event bus subscriber channels.
	// A larger buffer reduces the chance of dropping events during bursty emissions.
	StreamChannelBufferSize = 256
)

// ReactorConfig holds the configuration for creating a Reactor.
// Generation parameters are aligned with core.ModelConfig for full LLM control.
type ReactorConfig struct {
	APIKey     string
	BaseURL    string
	AuthToken  string
	Model      string
	ClientType gochat.ClientType

	Temperature      float64
	TopP             float64
	TopK             int
	PresencePenalty  float64
	FrequencyPenalty float64
	MaxTokens        int

	SystemPrompt  string
	MaxIterations int

	Logger core.Logger // Unified logging interface (optional, defaults to slog)

	IsLocal          bool
	AsyncToolTimeout time.Duration // Timeout for async tool execution (default 5 minutes)
}

// Merge combines two ReactorConfig values, using non-zero values from override
// to replace the corresponding fields in the receiver.
// This allows partial configuration overrides without affecting unset fields.
//
// Special cases:
//   - Empty strings are not merged (preserves original)
//   - Zero-value numeric fields are not merged (preserves original)
//   - PresencePenalty and FrequencyPenalty: 0 is a valid value, so they use != 0 check
//   - IsLocal and AsyncToolTimeout: always merged if explicitly set in override
func (c ReactorConfig) Merge(override ReactorConfig) ReactorConfig {
	if override.Model != "" {
		c.Model = override.Model
	}
	if override.APIKey != "" {
		c.APIKey = override.APIKey
	}
	if override.BaseURL != "" {
		c.BaseURL = override.BaseURL
	}
	if override.AuthToken != "" {
		c.AuthToken = override.AuthToken
	}
	if override.Temperature > 0 {
		c.Temperature = override.Temperature
	}
	if override.TopP > 0 {
		c.TopP = override.TopP
	}
	if override.TopK > 0 {
		c.TopK = override.TopK
	}
	if override.PresencePenalty != 0 {
		c.PresencePenalty = override.PresencePenalty
	}
	if override.FrequencyPenalty != 0 {
		c.FrequencyPenalty = override.FrequencyPenalty
	}
	if override.MaxTokens > 0 {
		c.MaxTokens = override.MaxTokens
	}
	if override.IsLocal {
		c.IsLocal = override.IsLocal
	}
	if override.AsyncToolTimeout > 0 {
		c.AsyncToolTimeout = override.AsyncToolTimeout
	}
	return c
}

// RunResult holds the complete output of a Run invocation.
type RunResult struct {
	Answer            string        `json:"answer" yaml:"answer"`
	Steps             []Step        `json:"steps,omitempty" yaml:"steps,omitempty"`
	TotalIterations   int           `json:"total_iterations" yaml:"total_iterations"`
	TerminationReason string        `json:"termination_reason,omitempty" yaml:"termination_reason,omitempty"`
	TokensUsed        int           `json:"tokens_used,omitempty" yaml:"tokens_used,omitempty"`
	TotalDuration     time.Duration `json:"total_duration_ms,omitempty" yaml:"total_duration_ms,omitempty"`
}

// Runner is the public interface for the T-A-O reactor.
type Runner interface {
	Run(ctx context.Context, input string, history ConversationHistory) (*RunResult, error)
}

var _ Runner = (*Reactor)(nil)

// Reactor is the core T-A-O (Think-Act-Observe) execution engine.
// It implements the Runner, RegistryHub, SessionManager, and TAOExecutor interfaces.
//
// Reactor orchestrates the complete agent loop:
//  1. Think: Call LLM with context → parse response into Thought
//  2. Act: Execute tool calls or generate answer based on Thought
//  3. Observe: Evaluate results → check termination conditions
//  4. Repeat until termination or max iterations reached
//
// Thread Safety:
//   - Public methods are safe for concurrent use where documented
//   - Internal state uses RWMutex for cached data (cachedLLMTools)
//   - Hook lists are immutable after construction (sort once at setup)
//
// Lifecycle:
//
//	Created via NewReactor() with ReactorConfig and ReactorOptions.
//	Use Run() to execute a single user request through the T-A-O loop.
//	Use CloneReactor() to create child reactors for sub-agent delegation.
type Reactor struct {
	// config holds the LLM generation parameters and operational settings.
	config ReactorConfig

	// toolRegistry stores all available tools (built-in + registered).
	toolRegistry core.ToolRegistry

	// toolExecutor executes tool calls with permission checking and result limits.
	toolExecutor core.ToolExecutor

	// skillRegistry stores loaded skills for domain-specific capabilities.
	skillRegistry core.SkillRegistry

	// ruleRegistry stores behavioral rules injected into system prompt (optional).
	ruleRegistry core.RuleRegistry

	// memory provides knowledge retrieval for grounding LLM responses (optional).
	memory core.Memory

	// llmCaller handles all LLM API interactions including streaming and token management.
	llmCaller *LLMCaller

	// prompt builds the structured system prompt from multiple sections.
	prompt *Prompt

	// askPermission manages user approval for sensitive tool operations.
	askPermission *tools.AskPermission

	// eventBus publishes ReactEvent messages to external subscribers (UI, loggers, etc.).
	eventBus EventBus

	// resultStore accumulates tool execution results for cross-reference.
	resultStore *core.ResultStore

	// kvStore provides session-scoped key-value storage for tool state sharing.
	kvStore core.KVStore

	// fileStore provides session-scoped file storage for temp files and drafts.
	fileStore core.FileStore

	// SpawnFunc creates sub-agents for the delegate tool.
	// Set by Agent after Reactor creation to avoid circular deps.
	SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

	// cachedLLMTools caches the LLM-ready tool definitions.
	// The full tool registry is converted once after all tools are registered
	// and reused across T-A-O cycles to avoid per-round conversion overhead.
	// Invalidated when RegisterTool is called after construction.
	cachedLLMTools []gochatcore.Tool
	cacheMu        sync.RWMutex

	// Directory context (Design-time safety: set during initialization from setup)
	projectDir string // Layer 2: Project working directory (always non-empty after init)
	sessionDir string // Layer 3: Session sandbox directory

	// contentReplacementState tracks which tool results have been replaced with previews.
	contentReplacementState *core.ContentReplacementState

	// toolResultBudgetEnforcer applies size limits to tool results (Phase 1: per-tool, Phase 2: aggregate).
	toolResultBudgetEnforcer *ToolResultBudgetEnforcer

	// asyncToolTimeout is the timeout for async tool execution (default 5 minutes)
	asyncToolTimeout time.Duration

	// Hook 集合 — 构建时按 priority 排序，运行时只读
	thoughtHooks     []ThoughtHook
	toolHooks        []ToolHook
	observationHooks []ObservationHook
}

// EventBus returns the event bus for subscribing to reactor events.
func (r *Reactor) EventBus() EventBus { return r.eventBus }

// Memory returns the memory instance for knowledge retrieval (may be nil if not configured).
func (r *Reactor) Memory() core.Memory { return r.memory }

// Prompt returns the prompt builder for system prompt generation.
func (r *Reactor) Prompt() *Prompt { return r.prompt }

// SetAskPermission replaces the permission checker instance.
// Use this to inject a pre-configured AskPermission with custom responders.
func (r *Reactor) SetAskPermission(p *tools.AskPermission) { r.askPermission = p }

// PermissionResponder returns the PermissionResponder for the AskPermission checker,
// which allows external code (e.g., TUI) to respond to pending permission requests.
func (r *Reactor) PermissionResponder() core.PermissionResponder { return r.askPermission }

// AskPermission returns the *tools.AskPermission instance for hook wiring.
func (r *Reactor) AskPermission() *tools.AskPermission { return r.askPermission }

// Logger returns the reactor's logger instance.
func (r *Reactor) Logger() core.Logger { return r.getLogger() }

// BudgetEnforcer returns the tool result budget enforcer.
func (r *Reactor) BudgetEnforcer() *ToolResultBudgetEnforcer { return r.toolResultBudgetEnforcer }

// RegisterThoughtHooks appends thought hooks and re-sorts by priority.
func (r *Reactor) RegisterThoughtHooks(hooks ...ThoughtHook) {
	r.thoughtHooks = append(r.thoughtHooks, hooks...)
	sortByPriority(r.thoughtHooks)
}

// RegisterToolHooks appends tool hooks and re-sorts by priority.
func (r *Reactor) RegisterToolHooks(hooks ...ToolHook) {
	r.toolHooks = append(r.toolHooks, hooks...)
	sortByPriority(r.toolHooks)
}

// RegisterObservationHooks appends observation hooks and re-sorts by priority.
func (r *Reactor) RegisterObservationHooks(hooks ...ObservationHook) {
	r.observationHooks = append(r.observationHooks, hooks...)
	sortByPriority(r.observationHooks)
}

// sortByPriority 是按 priority 排序 hook 切片的泛型辅助函数。
// 三条 hook 链各自独立调用此函数排序。
func sortByPriority[T interface{ Priority() int }](hooks []T) {
	sort.SliceStable(hooks, func(i, j int) bool {
		return hooks[i].Priority() < hooks[j].Priority()
	})
}

// ── Hook 执行器 ─────────────────────────────────────────────────────────────

// execThoughtHooksBefore 串行执行所有 ThoughtHook.Before。
func (r *Reactor) execThoughtHooksBefore(ctx *ReactContext, input *CallInput) (result HookResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("thought hook panicked", fmt.Errorf("%v", p))
			result = HookResult{Error: fmt.Errorf("thought hook panic: %v", p)}
		}
	}()
	for _, h := range r.thoughtHooks {
		result = h.Before(ctx, input)
		if result.IsTerminal() {
			return result
		}
	}
	return HookResult{}
}

// execThoughtHooksAfter 串行执行所有 ThoughtHook.After。
func (r *Reactor) execThoughtHooksAfter(ctx *ReactContext, thought *Thought) (result HookResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("thought hook panicked", fmt.Errorf("%v", p))
			result = HookResult{Error: fmt.Errorf("thought hook panic: %v", p)}
		}
	}()
	for _, h := range r.thoughtHooks {
		result = h.After(ctx, thought)
		if result.IsTerminal() {
			r.notifyThoughtAbort(ctx, result.AbortReason)
			return result
		}
	}
	return HookResult{}
}

// notifyThoughtAbort notifies all thought hooks in reverse priority order of an abort.
// This allows hooks to clean up resources or undo partial changes.
func (r *Reactor) notifyThoughtAbort(ctx *ReactContext, reason string) {
	for i := len(r.thoughtHooks) - 1; i >= 0; i-- {
		r.thoughtHooks[i].Abort(ctx, reason)
	}
}

// notifyToolAbort notifies all tool hooks in reverse priority order of an abort.
// ToolHook.Abort only skips the current tool, not the entire loop.
func (r *Reactor) notifyToolAbort(ctx *ReactContext, reason string) {
	for i := len(r.toolHooks) - 1; i >= 0; i-- {
		r.toolHooks[i].Abort(ctx, reason)
	}
}

// execToolHooksWithAbort 对单个工具调用执行完整的 ToolHook.Before → 执行 → After 链。
// ToolHook 的 Abort 只跳过当前工具，不终止整个循环。
func (r *Reactor) execToolHooksWithAbort(ctx *ReactContext, call toolCall) (ret *ToolResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("tool hook panicked", fmt.Errorf("%v", p))
			if ret == nil {
				ret = &ToolResult{
					ToolName: call.name,
					Success:  false,
					Error:    fmt.Sprintf("tool hook panic: %v", p),
				}
			}
		}
	}()

	// Before 链
	for _, h := range r.toolHooks {
		result := h.Before(ctx, call.name, call.params)
		if result.Abort {
			r.notifyToolAbort(ctx, result.AbortReason)
			return &ToolResult{
				ToolName: call.name,
				Success:  false,
				Error:    "aborted: " + result.AbortReason,
			}
		}
		if result.Error != nil {
			return &ToolResult{ToolName: call.name, Error: result.Error.Error()}
		}
	}

	// 执行
	res, execErr := r.toolExecutor.Execute(ctx.Ctx(), call.name, call.params)
	tr := ToolResult{
		ToolName:   call.name,
		ToolCallID: call.toolCallID,
	}
	if execErr != nil {
		tr.Error = execErr.Error()
		tr.Success = false
	} else {
		tr.Result = res.Result
		tr.Duration = res.Duration
		tr.Success = res.Error == nil
		if res.Error != nil {
			tr.Error = res.Error.Error()
		}
	}

	// After 链
	for _, h := range r.toolHooks {
		result := h.After(ctx, &tr)
		if result.Abort {
			tr.Success = false
			tr.Error = "aborted: " + result.AbortReason
			r.notifyToolAbort(ctx, result.AbortReason)
			break
		}
		if result.Error != nil {
			tr.Error = result.Error.Error()
			break
		}
	}

	ret = &tr
	return
}

// execObservationHooksAfter 串行执行所有 ObservationHook.After。
// Observation hooks can modify the Observation and signal termination.
func (r *Reactor) execObservationHooksAfter(ctx *ReactContext, obs *Observation) (result HookResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("observation hook panicked", fmt.Errorf("%v", p))
			result = HookResult{Error: fmt.Errorf("observation hook panic: %v", p)}
		}
	}()
	for _, h := range r.observationHooks {
		result = h.After(ctx, obs)
		if result.IsTerminal() {
			r.notifyObservationAbort(ctx, result.AbortReason)
			return result
		}
	}
	return HookResult{}
}

// notifyObservationAbort notifies all observation hooks in reverse priority order of an abort.
func (r *Reactor) notifyObservationAbort(ctx *ReactContext, reason string) {
	for i := len(r.observationHooks) - 1; i >= 0; i-- {
		r.observationHooks[i].Abort(ctx, reason)
	}
}

// getLogger returns the injected Logger or default slog-based logger.
func (r *Reactor) getLogger() core.Logger {
	if r.config.Logger != nil {
		return r.config.Logger
	}
	return core.DefaultLogger()
}

// reactorSetup holds all optional configuration for Reactor construction.
// Populated by ReactorOption functions before being applied in NewReactor().
//
// This struct is internal — external configuration uses ReactorOption functions.
type reactorSetup struct {
	systemPrompt   string
	skipTools      map[string]bool
	skipAllBundled bool
	extraTools     []core.FuncTool
	excludeTools   []string
	resultLimits   core.ToolResultLimits
	tokenEstimator core.TokenEstimator
	eventBus       EventBus
	skillDirs      []string
	skills         []string
	memory         core.Memory
	mockLLM        MockLLMFunc
	sessionStore   core.SessionStore
	kvStore        core.KVStore
	fileStore      core.FileStore
	toolRegistry   core.ToolRegistry
	skillRegistry  core.SkillRegistry
	ruleRegistry   core.RuleRegistry
	prompt         *Prompt

	// Directory context (Design-time safety: guaranteed by Agent layer)
	projectDir string // Layer 2: Set via WithProjectDir() ReactorOption
	sessionDir string // Layer 3: Auto-resolved from SessionStore or set via WithSessionDir()

	// Sandbox management (Agent Native Design: 4-Layer Architecture)
	sandboxMgr *tools.SessionSandboxManager // Manages session-scoped sandbox isolation

	// Hook 注入
	thoughtHooks     []ThoughtHook
	toolHooks        []ToolHook
	observationHooks []ObservationHook
}

// applyDefaults sets reasonable defaults for zero-value config fields.
// Called at the start of NewReactor before options are applied.
func (r *Reactor) applyDefaults(config *ReactorConfig) {
	if config.MaxIterations <= 0 {
		config.MaxIterations = core.DefaultMaxSteps
	}
	if config.Temperature <= 0 {
		config.Temperature = core.DefaultTemperature
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = core.DefaultMaxTokens
	}
}

func (r *Reactor) initRegistries(setup *reactorSetup) {
	if setup.toolRegistry != nil {
		r.toolRegistry = setup.toolRegistry
	} else {
		r.toolRegistry = NewDefaultToolRegistry()
	}
	if setup.skillRegistry != nil {
		r.skillRegistry = setup.skillRegistry
	} else {
		r.skillRegistry = NewDefaultSkillRegistry()
	}
	r.ruleRegistry = setup.ruleRegistry

	if setup.eventBus != nil {
		r.eventBus = setup.eventBus
	} else {
		r.eventBus = NewEventBus()
	}

	r.resultStore = core.NewResultStore()
}

func (r *Reactor) initLLMCaller(config ReactorConfig, setup *reactorSetup) {
	llmCfg := LLMCallerConfig{
		ModelName:        config.Model,
		SystemPrompt:     config.SystemPrompt,
		Temperature:      config.Temperature,
		TopP:             config.TopP,
		TopK:             config.TopK,
		PresencePenalty:  config.PresencePenalty,
		FrequencyPenalty: config.FrequencyPenalty,
		MaxTokens:        config.MaxTokens,
		ClientType:       config.ClientType,
		Logger:           r.getLogger(), // ← 关键：注入 Logger 到 LLMCaller！
	}

	client := gochat.Client().Config(
		gochat.WithAPIKey(config.APIKey),
		gochat.WithBaseURL(config.BaseURL),
	)

	estimator := setup.tokenEstimator
	if estimator == nil {
		estimator = core.NewTokenEstimator()
	}

	var llmOpts []LLMCallerOption
	if setup.sessionStore != nil {
		llmOpts = append(llmOpts, WithLLMCallerSessionStore(setup.sessionStore))
	}
	if setup.mockLLM != nil {
		llmOpts = append(llmOpts, WithLLMCallerMock(setup.mockLLM))
	}

	r.llmCaller = NewLLMCaller(llmCfg, client, estimator, setup.sessionStore, llmOpts...)
}

func (r *Reactor) discoverAndLoadSkills(setup *reactorSetup) {

	for _, dir := range setup.skillDirs {
		loader := core.NewFileSystemSkillLoader(dir)
		skills, err := loader.Load()
		if err != nil {
			r.getLogger().Warn("failed to load skills", "dir", dir, "error", err)
			continue
		}
		for _, skill := range skills {
			if len(setup.skills) > 0 {
				match := false
				for _, name := range setup.skills {
					if skill.Name == name {
						match = true
						break
					}
				}
				if !match {
					continue
				}
			}
			if err := r.skillRegistry.RegisterSkill(skill); err != nil {
				r.getLogger().Warn("failed to register skill", "name", skill.Name, "error", err)
			}
		}
	}
}

func (r *Reactor) initToolExecutor(setup *reactorSetup) {
	r.toolExecutor = core.NewToolExecutor(
		r.toolRegistry,
		core.WithPermissionChecker(r.askPermission),
		core.WithResultLimits(setup.resultLimits),
		core.WithEventEmitter(func(e core.ReactEvent) {
			if r.eventBus != nil {
				r.eventBus.Emit(e)
			}
		}),
		core.WithResultStore(r.resultStore),
		core.WithKVStore(r.kvStore),
		core.WithFileStore(r.fileStore),
		core.WithLogger(r.getLogger()),
		core.WithProjectDirExecutor(setup.projectDir), // Layer 2: From Agent or ReactorOption
		core.WithSessionDirExecutor(setup.sessionDir), // Layer 3: From SessionStore or explicit
	)
}

type toolRegistration struct {
	name     string
	factory  func(mgr *tools.SessionSandboxManager) core.FuncTool
	skipName string
}

var defaultBundledTools = []toolRegistration{
	{"Grep", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewGrepTool() }, "Grep"},
	{"Glob", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewGlobTool() }, "Glob"},
	{"Read", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewReadTool() }, "Read"},
	{"Write", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewWriteTool() }, "Write"},
	{"FileEdit", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewFileEditTool() }, "FileEdit"},
	{"Bash", func(mgr *tools.SessionSandboxManager) core.FuncTool {
		if mgr != nil {
			return tools.NewBashToolWithSessionSandbox(mgr)
		}
		return tools.NewBashTool()
	}, "Bash"},
	{"RunScript", func(mgr *tools.SessionSandboxManager) core.FuncTool {
		if mgr != nil {
			return tools.NewRunScriptToolWithSessionSandbox(mgr)
		}
		return tools.NewRunScriptTool()
	}, "RunScript"},
	{"PowerShell", func(mgr *tools.SessionSandboxManager) core.FuncTool {
		if !tools.IsWindowsPlatform() {
			return nil
		}
		if mgr != nil {
			return tools.NewPowerShellToolWithSessionSandbox(mgr)
		}
		return tools.NewPowerShellTool()
	}, "PowerShell"},
	{"WebSearch", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewWebSearchTool() }, "WebSearch"},
	{"WebFetch", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewWebFetchTool() }, "WebFetch"},
	{"TodoWrite", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTodoWriteTool() }, "TodoWrite"},
	{"TodoRead", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTodoReadTool() }, "TodoRead"},
	{"TodoExecute", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTodoExecuteTool() }, "TodoExecute"},
	{"AskUser", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewAskUserTool() }, "AskUser"},
	{"Ls", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewLsTool() }, "Ls"},
	{"CollectResults", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewCollectResultsTool() }, "CollectResults"},
	{"TaskList", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTaskListTool() }, "TaskList"},
	{"TaskGet", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTaskGetTool() }, "TaskGet"},
	{"TaskUpdate", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTaskUpdateTool() }, "TaskUpdate"},
	{"TaskStop", func(_ *tools.SessionSandboxManager) core.FuncTool { return tools.NewTaskStopTool() }, "TaskStop"},
}

func (r *Reactor) registerBundledTools(setup *reactorSetup) {
	if setup.skipAllBundled {
		return
	}

	for _, reg := range defaultBundledTools {
		if setup.skipTools[reg.skipName] {
			continue
		}
		tool := reg.factory(setup.sandboxMgr)
		if tool == nil {
			continue
		}
		if err := r.RegisterTool(tool); err != nil {
			r.getLogger().Warn("failed to register bundled tool", "name", reg.name, "error", err)
		}
	}

	if !setup.skipTools["Skill"] {
		skillTool := tools.NewSkillTool(func(name string) (*core.Skill, error) {
			return r.skillRegistry.GetSkill(name)
		})
		if err := r.RegisterTool(skillTool); err != nil {
			r.getLogger().Warn("failed to register Skill tool", "error", err)
		}
	}

	r.registerOrchestrationTools(setup)
}

func (r *Reactor) registerOrchestrationTools(setup *reactorSetup) {
	spawn := r.SpawnFunc
	orchestrationTools := []struct {
		name string
		tool core.FuncTool
	}{
		{"Delegate", tools.NewDelegateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if spawn != nil {
				return spawn(ctx, agentName, task)
			}
			return "", fmt.Errorf("delegate: SpawnFunc not configured on reactor")
		})},
		{"TaskCreate", tools.NewTaskCreateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if spawn != nil {
				return spawn(ctx, agentName, task)
			}
			return "", fmt.Errorf("task_create: SpawnFunc not configured on reactor")
		})},
		{"TeamCreate", tools.NewTeamCreateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if spawn != nil {
				return spawn(ctx, agentName, task)
			}
			return "", fmt.Errorf("team_create: SpawnFunc not configured on reactor")
		})},
	}
	for _, ot := range orchestrationTools {
		if !setup.skipTools[ot.name] {
			if err := r.RegisterTool(ot.tool); err != nil {
				r.getLogger().Warn("failed to register orchestration tool", "name", ot.name, "error", err)
			}
		}
	}
}

func NewReactor(config ReactorConfig, opts ...ReactorOption) *Reactor {
	r := &Reactor{}

	r.applyDefaults(&config)

	setup := &reactorSetup{skipTools: make(map[string]bool)}
	for _, opt := range opts {
		opt(setup)
	}

	if setup.systemPrompt != "" {
		config.SystemPrompt = setup.systemPrompt
	}

	r.config = config
	r.memory = setup.memory
	r.prompt = setup.prompt
	// Initialize async tool timeout (default 5 minutes if not configured)
	if config.AsyncToolTimeout > 0 {
		r.asyncToolTimeout = config.AsyncToolTimeout
	} else {
		r.asyncToolTimeout = 5 * time.Minute
	}
	// Directory context (Design-time safety: copy from setup to Reactor)
	r.projectDir = setup.projectDir
	r.sessionDir = setup.sessionDir

	if setup.kvStore == nil {
		if kv, err := core.NewFileSystemKVStore(""); err == nil {
			r.kvStore = kv
		} else {
			r.getLogger().Warn("failed to initialize default KVStore, session data sharing disabled", "error", err)
		}
	} else {
		r.kvStore = setup.kvStore
	}
	if setup.fileStore == nil {
		if fs, err := core.NewFileSystemFileStore(""); err == nil {
			r.fileStore = fs
		} else {
			r.getLogger().Warn("failed to initialize default FileStore, session file sharing disabled", "error", err)
		}
	} else {
		r.fileStore = setup.fileStore
	}

	r.initRegistries(setup)
	r.initLLMCaller(config, setup)
	r.discoverAndLoadSkills(setup)
	// Initialize permission checker (used by tool executor)
	r.askPermission = tools.NewAskPermission()
	r.askPermission.SetEventEmitter(func(e core.ReactEvent) {
		if r.eventBus != nil {
			r.eventBus.Emit(e)
		}
	})
	r.initToolExecutor(setup)
	r.registerBundledTools(setup)

	// Initialize content replacement state and budget enforcer
	r.contentReplacementState = core.NewContentReplacementState()
	sessionDir := r.sessionDir
	if sessionDir == "" && r.fileStore != nil {
		sessionDir = r.fileStore.GetSessionPath("")
	}
	r.toolResultBudgetEnforcer = NewToolResultBudgetEnforcer(
		core.NewDiskResultPersister(sessionDir),
		core.DefaultToolResultLimits(),
		r.contentReplacementState,
		func(name string) *core.ToolInfo {
			if t, ok := r.toolRegistry.Get(name); ok {
				return t.Info()
			}
			return nil
		},
	)

	for _, tool := range setup.extraTools {
		if err := r.RegisterTool(tool); err != nil {
			r.getLogger().Warn("failed to register extra tool", "error", err)
		}
	}

	// Apply exclusions: remove tools from registry after all registration is done
	for _, name := range setup.excludeTools {
		if err := r.toolRegistry.Remove(name); err != nil {
			r.getLogger().Warn("failed to exclude tool", "name", name, "error", err)
		}
	}
	// Invalidate cached LLM tool definitions after exclusions
	if len(setup.excludeTools) > 0 {
		r.cacheMu.Lock()
		r.cachedLLMTools = nil
		r.cacheMu.Unlock()
	}

	// ── Hook 初始化 ──
	// 用户 hooks — 通过 Option 追加
	r.thoughtHooks = append(r.thoughtHooks, setup.thoughtHooks...)
	r.toolHooks = append(r.toolHooks, setup.toolHooks...)
	r.observationHooks = append(r.observationHooks, setup.observationHooks...)

	// 按 priority 排序（per-chain）
	sortByPriority(r.thoughtHooks)
	sortByPriority(r.toolHooks)
	sortByPriority(r.observationHooks)

	return r
}

func (r *Reactor) SkillRegistry() core.SkillRegistry       { return r.skillRegistry }
func (r *Reactor) ToolRegistry() core.ToolRegistry         { return r.toolRegistry }
func (r *Reactor) ToolExecutor() core.ToolExecutor         { return r.toolExecutor }
func (r *Reactor) RuleRegistry() core.RuleRegistry         { return r.ruleRegistry }
func (r *Reactor) SessionStore() core.SessionStore         { return r.llmCaller.SessionStore() }
func (r *Reactor) KVStore() core.KVStore                   { return r.kvStore }
func (r *Reactor) FileStore() core.FileStore               { return r.fileStore }
func (r *Reactor) ContextWindow() *core.ContextWindow      { return r.llmCaller.ContextWindow() }
func (r *Reactor) SetContextWindow(cw *core.ContextWindow) { r.llmCaller.SetContextWindow(cw) }
func (r *Reactor) SlideConfig() core.SlideConfig           { return r.llmCaller.SlideConfig() }
func (r *Reactor) EstimateTokens(content string) int {
	return r.llmCaller.Estimator().Estimate(content)
}

// SetModelConfig updates the reactor's LLM configuration at runtime.
// This propagates model parameters to the LLMCaller and recreates the
// gochat client when API key or base URL change.
// Conversation history and context window are preserved.
func (r *Reactor) SetModelConfig(model core.ModelConfig) {
	// Update reactor config
	r.config.Model = model.Name
	r.config.Temperature = model.Temperature
	r.config.TopP = model.TopP
	r.config.TopK = int(model.TopK)
	r.config.PresencePenalty = model.RepetitionPenalty
	r.config.FrequencyPenalty = model.FrequencyPenalty
	r.config.MaxTokens = int(model.MaxTokens)
	r.config.MaxIterations = model.MaxTurns
	r.config.IsLocal = model.IsLocal

	// Update connection parameters
	if model.APIKey != "" {
		r.config.APIKey = model.APIKey
	}
	if model.BaseURL != "" {
		r.config.BaseURL = model.BaseURL
	}
	if model.AuthToken != "" {
		r.config.AuthToken = model.AuthToken
	}

	// Recreate gochat client to pick up API key / base URL changes
	client := gochat.Client().Config(
		gochat.WithAPIKey(r.config.APIKey),
		gochat.WithBaseURL(r.config.BaseURL),
	)
	r.llmCaller.SetClient(client)

	// Update LLMCaller config
	r.llmCaller.SetConfig(LLMCallerConfig{
		ModelName:        r.config.Model,
		SystemPrompt:     r.config.SystemPrompt,
		Temperature:      r.config.Temperature,
		TopP:             r.config.TopP,
		TopK:             r.config.TopK,
		PresencePenalty:  r.config.PresencePenalty,
		FrequencyPenalty: r.config.FrequencyPenalty,
		MaxTokens:        r.config.MaxTokens,
		ClientType:       r.config.ClientType,
		Logger:           r.config.Logger,
	})
}

func (r *Reactor) RegisterTool(tool core.FuncTool) error {
	r.cacheMu.Lock()
	r.cachedLLMTools = nil // invalidate cache
	r.cacheMu.Unlock()
	return r.toolRegistry.Register(tool)
}

// getLLMTools returns cached LLM-ready tool definitions, building them on first call.
func (r *Reactor) getLLMTools() []gochatcore.Tool {
	r.cacheMu.RLock()
	cached := r.cachedLLMTools
	r.cacheMu.RUnlock()
	if cached != nil {
		return cached
	}

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	// Double-check after acquiring write lock
	if r.cachedLLMTools != nil {
		return r.cachedLLMTools
	}
	allToolInfos := core.ToToolInfos(r.toolRegistry.All())
	r.cachedLLMTools = ToolInfosToLLMTools(allToolInfos)
	return r.cachedLLMTools
}

// CloneReactor creates a child Reactor that inherits all registries, infrastructure,
// and execution pipeline from the parent, but with an independent config, task manager,
// LLM client, and conversation context.
//
// Thread Safety Guarantee:
//
//	Safe for concurrent use (parent + child running simultaneously):
//	  - toolRegistry, skillRegistry: read-only after setup ✅
//	  - eventBus: internally synchronized (InProcessEventBus uses RWMutex) ✅
//	  - kvStore, fileStore: externally synchronized ✅
//	  - thoughtHooks, toolHooks, observationHooks: immutable after setup ✅
//
//	Requires caution (shared mutable state):
//	  - toolExecutor: may contain internal state (permission checks, result limits).
//	    If parent/child execute tools concurrently, ensure the implementation is thread-safe.
//	  - toolResultBudgetEnforcer: shares ContentReplacementState reference.
//	    The state is cloned at creation time, but runtime mutations are shared.
//	    For complete isolation, consider recreating the enforcer for the child.
//
// Independent (new instances for child):
//   - config (Model, SystemPrompt, Temperature, etc. — can override)
//   - llmCaller (child's own LLM caller with independent context window)
//   - contextWindow (child's own conversation history)
//   - askPermission (child's own permission checker with independent event emitter)
//   - asyncToolTimeout (copied from parent config, can be overridden)
//
// Use case: SubAgent creation where the child needs parent's tools/skills/memory
// but runs its own T-A-O loop with possibly a different model or system prompt.
func (r *Reactor) CloneReactor(configOverride ReactorConfig) *Reactor {
	childConfig := r.config.Merge(configOverride)

	// SystemPrompt: always use override value directly, or clear to prevent identity leakage.
	// Merge() skips empty strings, but for SystemPrompt, empty means "explicitly clear".
	childConfig.SystemPrompt = configOverride.SystemPrompt

	child := &Reactor{
		config:                   childConfig,
		toolRegistry:             r.toolRegistry,
		toolExecutor:             r.toolExecutor,
		skillRegistry:            r.skillRegistry,
		ruleRegistry:             r.ruleRegistry,
		memory:                   r.memory,
		eventBus:                 r.eventBus,
		kvStore:                  r.kvStore,
		fileStore:                r.fileStore,
		contentReplacementState:  r.contentReplacementState.Clone(),
		toolResultBudgetEnforcer: r.toolResultBudgetEnforcer,
		thoughtHooks:             r.thoughtHooks,
		toolHooks:                r.toolHooks,
		observationHooks:         r.observationHooks,
		asyncToolTimeout:         r.asyncToolTimeout,
	}

	// Clone LLMCaller with parent's shared infrastructure but independent client/context
	child.llmCaller = r.cloneLLMCallerForChild(childConfig)

	child.askPermission = tools.NewAskPermission()
	child.askPermission.SetEventEmitter(func(e core.ReactEvent) {
		if child.eventBus != nil {
			child.eventBus.Emit(e)
		}
	})

	return child
}

// cloneLLMCallerForChild creates a new LLMCaller for CloneReactor,
// sharing the parent's infrastructure (tokenEstimator, sessionStore, mockLLM)
// but with its own client and context window.
func (r *Reactor) cloneLLMCallerForChild(childConfig ReactorConfig) *LLMCaller {
	llmCfg := LLMCallerConfig{
		ModelName:        childConfig.Model,
		SystemPrompt:     childConfig.SystemPrompt,
		Temperature:      childConfig.Temperature,
		TopP:             childConfig.TopP,
		TopK:             childConfig.TopK,
		PresencePenalty:  childConfig.PresencePenalty,
		FrequencyPenalty: childConfig.FrequencyPenalty,
		MaxTokens:        childConfig.MaxTokens,
		ClientType:       childConfig.ClientType,
	}

	client := gochat.Client().Config(
		gochat.WithAPIKey(childConfig.APIKey),
		gochat.WithBaseURL(childConfig.BaseURL),
	)

	parentCaller := r.llmCaller
	var llmOpts []LLMCallerOption
	if parentCaller != nil {
		if parentCaller.SessionStore() != nil {
			llmOpts = append(llmOpts, WithLLMCallerSessionStore(parentCaller.SessionStore()))
		}
		return NewLLMCaller(llmCfg, client, parentCaller.Estimator(), parentCaller.SessionStore(), llmOpts...)
	}

	// Fallback: create standalone LLMCaller for child without parent infrastructure
	return NewLLMCaller(llmCfg, client, nil, nil, llmOpts...)
}

func (r *Reactor) Run(ctx context.Context, input string, history ConversationHistory) (*RunResult, error) {
	reactCtx := NewReactContext(ctx, input, history, r.config.MaxIterations)

	if r.eventBus != nil {
		reactCtx.emitEvent = r.eventBus.Emit
	}

	if cw := r.ContextWindow(); cw != nil && cw.SessionID != "" {
		reactCtx.SessionID = cw.SessionID
	}

	return r.runLoop(reactCtx, 0, time.Now())
}

// persistStep records a T-A-O cycle step into history and persistent storage.
// Uses structured messages (v2 format) instead of XML:
//
//	assistant: "Thought: <reasoning>\nDecision: <decision>"
//	tool:      "<tool_name> returned: <result>"     (if tool was called)
//	tool:      "<tool_name> error: <error>"           (if tool errored)
func (r *Reactor) persistStep(reactCtx *ReactContext, cycleStart time.Time) {
	// Apply budget enforcement before results enter conversation history.
	// Large results are persisted and replaced with previews (Phase 1),
	// then aggregate budget is checked (Phase 2).
	if reactCtx.LastThought.Decision == DecisionAct && r.toolResultBudgetEnforcer != nil {
		reactCtx.LastAction.Results = r.toolResultBudgetEnforcer.Enforce(
			reactCtx.LastAction.Results,
		)
	}

	step := Step{
		Iteration:   reactCtx.CurrentIteration + 1,
		Thought:     *reactCtx.LastThought,
		Action:      *reactCtx.LastAction,
		Observation: *reactCtx.LastObservation,
		Timestamp:   time.Now(),
		Duration:    time.Since(cycleStart),
	}
	reactCtx.AppendHistory(step)

	reactCtx.EmitEvent(core.CycleEnd, core.CycleInfo{
		Iteration: reactCtx.CurrentIteration + 1,
		Duration:  time.Since(cycleStart),
	})

	// Structured assistant message: thought content
	thoughtMsg := fmt.Sprintf("Thought: %s\nDecision: %s", reactCtx.LastThought.Reasoning, reactCtx.LastThought.Decision)
	reactCtx.AddMessage("assistant", thoughtMsg)
	r.persistStepToStore(reactCtx.Ctx(), "assistant", thoughtMsg)

	// Structured tool messages: one per tool result (with correct tool_call_id)
	if reactCtx.LastThought.Decision == DecisionAct {
		for _, tr := range reactCtx.LastAction.Results {
			toolCallID := lookUpToolCallID(reactCtx.LastThought, tr.ToolName)
			toolMsg := tr.ToolResultSummary()
			reactCtx.AddToolMessage("tool", toolMsg, toolCallID)
			r.persistStepToStore(reactCtx.Ctx(), "tool", toolMsg)
		}
	}
}

// buildResultFromContext constructs a RunResult from the ReactContext state.
func (r *Reactor) buildResultFromContext(reactCtx *ReactContext, totalTokens int, totalInputTokens int, runStart time.Time) *RunResult {
	answer := extractAnswer(reactCtx)
	if answer != "" {
		reactCtx.EmitEvent(core.FinalAnswer, answer)
	}

	totalDuration := time.Since(runStart)

	summary := buildExecutionSummary(reactCtx, totalTokens, totalDuration)
	reactCtx.EmitEvent(core.ExecutionSummary, summary)

	totalOutputTokens := totalTokens - totalInputTokens
	taskSummary := buildTaskSummary(answer, totalInputTokens, totalOutputTokens, reactCtx.CurrentIteration,
		summary.ToolCalls, totalDuration, reactCtx.TerminationReason)
	reactCtx.EmitEvent(core.TaskSummary, taskSummary)

	return &RunResult{
		Answer:            answer,
		Steps:             reactCtx.History,
		TotalIterations:   reactCtx.CurrentIteration,
		TerminationReason: reactCtx.TerminationReason,
		TokensUsed:        totalTokens,
		TotalDuration:     totalDuration,
	}
}

func extractAnswer(reactCtx *ReactContext) string {
	if reactCtx.LastAction != nil && len(reactCtx.LastAction.Results) > 0 {
		// When the answer comes from a DecisionAnswer action, the result is
		// stored as ToolResult{ToolName:"answer", Result:text}.  Use the raw
		// Result directly rather than Summary() which would prefix "[answer]".
		for _, r := range reactCtx.LastAction.Results {
			if r.ToolName == "answer" {
				return r.Result
			}
		}
		return reactCtx.LastAction.Summary()
	}
	if reactCtx.LastThought != nil && reactCtx.LastThought.FinalAnswer != "" {
		return reactCtx.LastThought.FinalAnswer
	}
	if reactCtx.LastObservation != nil && reactCtx.LastObservation.Result != "" {
		return reactCtx.LastObservation.Result
	}
	if reactCtx.TerminationReason != "" {
		return fmt.Sprintf("<task-terminated>%s</task-terminated>", reactCtx.TerminationReason)
	}
	return ""
}

func buildExecutionSummary(reactCtx *ReactContext, totalTokens int, totalDuration time.Duration) core.ExecutionSummaryData {
	summary := core.ExecutionSummaryData{
		TotalIterations:   reactCtx.CurrentIteration,
		TotalDuration:     totalDuration,
		TokensUsed:        totalTokens,
		TerminationReason: reactCtx.TerminationReason,
	}
	summary.ToolsUsed = collectUniqueToolNames(reactCtx.History)
	summary.ToolCalls = 0
	for _, step := range reactCtx.History {
		if step.Thought.Decision == DecisionAct {
			summary.ToolCalls += len(step.Action.Results)
		}
	}
	return summary
}

func buildTaskSummary(answer string, inputTokens int, outputTokens int, iterations int, toolCalls int, duration time.Duration, terminationReason string) core.TaskSummaryData {
	data := core.TaskSummaryData{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	if iterations > 1 || toolCalls > 0 {
		toolWord := "tool calls"
		if toolCalls == 1 {
			toolWord = "tool call"
		}
		data.Summary = fmt.Sprintf(
			"Completed %d iteration(s) with %d %s in %s. Termination reason: %s.",
			iterations, toolCalls, toolWord,
			duration.Round(time.Millisecond), terminationReason,
		)
	} else if answer != "" {
		data.Summary = fmt.Sprintf("Direct answer provided. %s", terminationReason)
	}
	return data
}

func (r *Reactor) abortCycle(reactCtx *ReactContext, phase string, err error,
	cycleStart time.Time, cycleNum int, sessionID string) {
	reactCtx.TerminationReason = fmt.Sprintf("%s error: %v", phase, err)
	reactCtx.EmitEvent(core.Error, reactCtx.TerminationReason)
	r.getLogger().Error("cycle abort", err,
		"session_id", sessionID,
		"iteration", cycleNum,
		"phase", phase,
		"elapsed_ms", time.Since(cycleStart).Milliseconds(),
	)
}

func (r *Reactor) runLoop(reactCtx *ReactContext, initialTokens int, runStart time.Time) (ret *RunResult, retErr error) {
	defer func() {
		if p := recover(); p != nil {
			retErr = fmt.Errorf("runLoop panicked: %v", p)
			r.getLogger().Error("runLoop panicked", retErr,
				"session_id", r.resolveSessionID(reactCtx),
				"panic", p,
			)
		}
	}()

	totalTokens := initialTokens
	totalInputTokens := initialTokens
	sessionID := r.resolveSessionID(reactCtx)
	r.getLogger().Info("run loop start",
		"session_id", sessionID,
		"max_iterations", reactCtx.MaxIterations,
		"input_preview", Truncate(reactCtx.Input, 80),
	)

	for reactCtx.CurrentIteration < reactCtx.MaxIterations {
		// TerminationReason 由 hook (PreCheckHook / ConvergenceHook) 设置
		if reactCtx.TerminationReason != "" {
			reactCtx.IsTerminated = true
			r.getLogger().Info("run loop terminated",
				"session_id", sessionID,
				"iteration", reactCtx.CurrentIteration+1,
				"reason", reactCtx.TerminationReason,
			)
			break
		}

		cycleStart := time.Now()
		cycleNum := reactCtx.CurrentIteration + 1
		r.toolExecutor.ResetCycle()

		inputTokens, outputTokens, err := r.Think(reactCtx)
		totalTokens += inputTokens + outputTokens
		totalInputTokens += inputTokens
		reactCtx.CurrentInputTokens = inputTokens
		if err != nil {
			r.abortCycle(reactCtx, "think", err, cycleStart, cycleNum, sessionID)
			break
		}
		// PreCheckHook 中止（ctx cancelled/已达上限）→ 跳过 Act/Observe
		if reactCtx.TerminationReason != "" {
			reactCtx.IsTerminated = true
			r.getLogger().Info("run loop terminated",
				"session_id", sessionID,
				"reason", reactCtx.TerminationReason,
			)
			break
		}

		if err := r.Act(reactCtx); err != nil {
			r.abortCycle(reactCtx, "act", err, cycleStart, cycleNum, sessionID)
			break
		}

		if err := r.Observe(reactCtx); err != nil {
			r.abortCycle(reactCtx, "observe", err, cycleStart, cycleNum, sessionID)
			break
		}

		r.persistStep(reactCtx, cycleStart)
		reactCtx.CurrentIteration++

		r.getLogger().Info("cycle end",
			"session_id", sessionID,
			"iteration", cycleNum,
			"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			"input_tokens", inputTokens,
		)
	}

	result := r.buildResultFromContext(reactCtx, totalTokens, totalInputTokens, runStart)
	r.getLogger().Info("run loop done",
		"session_id", sessionID,
		"total_iterations", result.TotalIterations,
		"total_tokens", totalTokens,
		"total_elapsed_ms", time.Since(runStart).Milliseconds(),
		"termination_reason", result.TerminationReason,
	)

	return result, nil
}

// persistStepToStore persists an intermediate step message to the session store
// and tracks it in the LLMCaller's context window for token budget management.
func (r *Reactor) persistStepToStore(ctx context.Context, role, content string) {
	ss := r.llmCaller.SessionStore()
	cw := r.llmCaller.ContextWindow()
	if ss == nil || cw == nil {
		r.llmCaller.AddContextMessage(role, content)
		return
	}

	agentName := cw.Role
	msg := core.Message{Role: role, Content: content, Timestamp: time.Now().Unix()}
	r.llmCaller.AddContextMessage(role, content)
	if err := ss.Append(ctx, cw.SessionID, agentName, msg); err != nil {
		r.getLogger().Warn("failed to persist step to session store", "session_id", cw.SessionID, "role", role, "error", err)
	}
}

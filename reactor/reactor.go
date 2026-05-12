package reactor

import (
	"context"
	"fmt"
	"sync"
	"time"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/tools"
)

const (
	historyTokenBudgetRatio = 0.7
	StreamChannelBufferSize = 256

	defaultMaxHistoryTurns = 8
	tokensPerTurnEstimate  = 500
	minMaxHistoryTurns     = 3
	maxMaxHistoryTurns     = 20
)

func maxHistoryTurnsForConfig(maxTokens int) int {
	if maxTokens <= 0 {
		return defaultMaxHistoryTurns
	}
	estimated := int(float64(maxTokens) * historyTokenBudgetRatio / float64(tokensPerTurnEstimate))
	if estimated < minMaxHistoryTurns {
		return minMaxHistoryTurns
	}
	if estimated > maxMaxHistoryTurns {
		return maxMaxHistoryTurns
	}
	return estimated
}

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

	IsLocal bool
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
// External consumers only need Run/RunFromSnapshot for task execution.
type Runner interface {
	Run(ctx context.Context, input string, history ConversationHistory) (*RunResult, error)
	RunFromSnapshot(ctx context.Context, snapshot *RunSnapshot, newInput string) (*RunResult, error)
}

// TAORunner extends Runner with individual T-A-O phase access.
// Used by test code and internal orchestration that needs fine-grained control.
type TAORunner interface {
	Runner
	Think(ctx *ReactContext) (int, error)
	Act(ctx *ReactContext) error
	Observe(ctx *ReactContext) error
	CheckTermination(ctx *ReactContext) (bool, string)
}

var _ TAORunner = (*Reactor)(nil)

type Reactor struct {
	config        ReactorConfig
	toolRegistry  core.ToolRegistry
	toolExecutor  core.ToolExecutor
	skillRegistry core.SkillRegistry
	ruleRegistry  core.RuleRegistry

	memory    core.Memory
	llmCaller *LLMCaller
	prompt    *Prompt

	interactionHandler HumanInteractionHandler
	askPermission      *tools.AskPermission
	eventBus           EventBus

	resultStore *core.ResultStore
	kvStore     core.KVStore
	fileStore   core.FileStore

	// SpawnFunc creates sub-agents for the delegate tool.
	// Set by Agent after Reactor creation to avoid circular deps.
	SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

	pauseRequested bool
	pauseMu        sync.Mutex

	snapshotHolder struct {
		sync.RWMutex
		snap *RunSnapshot
	}

	// cachedLLMTools caches the LLM-ready tool definitions.
	// The full tool registry is converted once after all tools are registered
	// and reused across T-A-O cycles to avoid per-round conversion overhead.
	// Invalidated when RegisterTool is called after construction.
	cachedLLMTools []gochatcore.Tool
	cacheMu        sync.RWMutex

	// Agent orchestration dependencies (set by Agent, zero-value safe when nil)
	agentRegistry tools.AgentDefinitionRegistry
	runtimeDir    *core.RuntimeDirectory
	modelRegistry core.ModelRegistry

	auditLogger core.AuditLogger

	// Directory context (Design-time safety: set during initialization from setup)
	projectDir string // Layer 2: Project working directory (always non-empty after init)
	sessionDir string // Layer 3: Session sandbox directory
}

func (r *Reactor) EventBus() EventBus { return r.eventBus }

func (r *Reactor) InteractionHandler() HumanInteractionHandler { return r.interactionHandler }

func (r *Reactor) Memory() core.Memory { return r.memory }

func (r *Reactor) Prompt() *Prompt { return r.prompt }

func (r *Reactor) SetPauseRequested() {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	r.pauseRequested = true
}

func (r *Reactor) TakeSnapshot() *RunSnapshot {
	r.pauseMu.Lock()
	defer r.pauseMu.Unlock()
	r.pauseRequested = false
	return r.snapshotHolder.snap
}

func (r *Reactor) setSnapshot(snap *RunSnapshot) {
	r.snapshotHolder.Lock()
	defer r.snapshotHolder.Unlock()
	r.snapshotHolder.snap = snap
}

func (r *Reactor) getSnapshot() *RunSnapshot {
	r.snapshotHolder.Lock()
	defer r.snapshotHolder.Unlock()
	snap := r.snapshotHolder.snap
	r.snapshotHolder.snap = nil
	return snap
}

func (r *Reactor) clearSnapshot() {
	r.snapshotHolder.Lock()
	defer r.snapshotHolder.Unlock()
	r.snapshotHolder.snap = nil
}

func (r *Reactor) ConsumeSnapshot() *RunSnapshot { return r.getSnapshot() }

func (r *Reactor) PeekSnapshot() *RunSnapshot {
	r.snapshotHolder.RLock()
	defer r.snapshotHolder.RUnlock()
	return r.snapshotHolder.snap
}

func (r *Reactor) SetAskPermission(p *tools.AskPermission) { r.askPermission = p }

// getLogger returns the injected Logger or default slog-based logger.
func (r *Reactor) getLogger() core.Logger {
	if r.config.Logger != nil {
		return r.config.Logger
	}
	return core.DefaultLogger()
}

type reactorSetup struct {
	systemPrompt   string
	skipTools      map[string]bool
	skipAllBundled bool
	extraTools     []core.FuncTool
	excludeTools   []string
	resultLimits   core.ToolResultLimits
	tokenEstimator core.TokenEstimator
	eventBus       EventBus
	mcpRegistry    *core.MCPToolRegistry
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
	agentRegistry  tools.AgentDefinitionRegistry
	runtimeDir     *core.RuntimeDirectory
	modelRegistry  core.ModelRegistry
	auditLogger    core.AuditLogger

	// Directory context (Design-time safety: guaranteed by Agent layer)
	projectDir string // Layer 2: Set via WithProjectDir() ReactorOption
	sessionDir string // Layer 3: Auto-resolved from SessionStore or set via WithSessionDir()

	// Sandbox management (Agent Native Design: 4-Layer Architecture)
	sandboxMgr *tools.SessionSandboxManager // Manages session-scoped sandbox isolation
}

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

func (r *Reactor) initInteractionHandler() {
	r.interactionHandler = NewDefaultInteractionHandler(func(e core.ReactEvent) {
		if r.eventBus != nil {
			r.eventBus.Emit(e)
		}
	})
	if err := r.RegisterTool(tools.NewAskUserTool()); err != nil {
		r.getLogger().Warn("failed to register ask_user tool", "error", err)
	}

	r.askPermission = tools.NewAskPermission()
	r.askPermission.SetEventEmitter(func(e core.ReactEvent) {
		if r.eventBus != nil {
			r.eventBus.Emit(e)
		}
	})
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

func (r *Reactor) registerBundledTools(setup *reactorSetup) {
	if setup.skipAllBundled {
		return
	}

	bundledTools := []struct {
		name string
		tool core.FuncTool
	}{
		{"Grep", tools.NewGrepTool()},
		{"Glob", tools.NewGlobTool()},
		{"Read", tools.NewReadTool()},
		{"Write", tools.NewWriteTool()},
		{"FileEdit", tools.NewFileEditTool()},
	}

	for _, bt := range bundledTools {
		if !setup.skipTools[bt.name] {
			if err := r.RegisterTool(bt.tool); err != nil {
				r.getLogger().Warn("failed to register bundled tool", "name", bt.name, "error", err)
			}
		}
	}

	if setup.sandboxMgr != nil {
		bashTool := tools.NewBashToolWithSessionSandbox(setup.sandboxMgr)
		if err := r.RegisterTool(bashTool); err != nil {
			r.getLogger().Warn("failed to register Bash tool with session sandbox", "error", err)
		}

		runScriptTool := tools.NewRunScriptToolWithSessionSandbox(setup.sandboxMgr)
		if err := r.RegisterTool(runScriptTool); err != nil {
			r.getLogger().Warn("failed to register RunScript tool with session sandbox", "error", err)
		}

		if tools.IsWindowsPlatform() && !setup.skipTools["PowerShell"] {
			psTool := tools.NewPowerShellToolWithSessionSandbox(setup.sandboxMgr)
			if err := r.RegisterTool(psTool); err != nil {
				r.getLogger().Warn("failed to register PowerShell tool with session sandbox", "error", err)
			}
		}
	} else {
		bashTool := tools.NewBashTool()
		if err := r.RegisterTool(bashTool); err != nil {
			r.getLogger().Warn("failed to register Bash tool (no session sandbox)", "error", err)
		}

		runScriptTool := tools.NewRunScriptTool()
		if err := r.RegisterTool(runScriptTool); err != nil {
			r.getLogger().Warn("failed to register RunScript tool (no session sandbox)", "error", err)
		}

		if tools.IsWindowsPlatform() && !setup.skipTools["PowerShell"] {
			if err := r.RegisterTool(tools.NewPowerShellTool()); err != nil {
				r.getLogger().Warn("failed to register PowerShell tool (no session sandbox)", "error", err)
			}
		}
	}

	remainingTools := []struct {
		name string
		tool core.FuncTool
	}{
		{"WebSearch", tools.NewWebSearchTool()},
		{"WebFetch", tools.NewWebFetchTool()},
		{"TodoWrite", tools.NewTodoWriteTool()},
		{"TodoRead", tools.NewTodoReadTool()},
		{"TodoExecute", tools.NewTodoExecuteTool()},
		{"AskUser", tools.NewAskUserTool()},
		{"Ls", tools.NewLsTool()},
		{"Crontab", tools.NewCrontabTool()},
		{"Delegate", tools.NewDelegateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if r.SpawnFunc != nil {
				return r.SpawnFunc(ctx, agentName, task)
			}
			return "", fmt.Errorf("delegate: SpawnFunc not configured on reactor")
		})},
		{"CollectResults", tools.NewCollectResultsTool()},
		{"Skill", tools.NewSkillTool(func(name string) (*core.Skill, error) {
			return r.skillRegistry.GetSkill(name)
		})},
		{"SkillCreate", tools.NewSkillCreateTool()},
		{"SkillList", tools.NewSkillListTool()},
		{"ModelList", tools.NewModelListTool(r.modelRegistry)},
		{"FindAgent", tools.NewFindAgentTool(r.runtimeDir)},
		{"Rank", tools.NewRankTool(r.runtimeDir)},
		{"CreateAgent", tools.NewCreateAgentTool(r.agentRegistry, r.runtimeDir)},
		{"TaskCreate", tools.NewTaskCreateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if r.SpawnFunc != nil {
				return r.SpawnFunc(ctx, agentName, task)
			}
			return "", fmt.Errorf("task_create: SpawnFunc not configured on reactor")
		})},
		{"TaskList", tools.NewTaskListTool()},
		{"TaskGet", tools.NewTaskGetTool()},
		{"TaskUpdate", tools.NewTaskUpdateTool()},
		{"TaskStop", tools.NewTaskStopTool()},
		{"TeamCreate", tools.NewTeamCreateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if r.SpawnFunc != nil {
				return r.SpawnFunc(ctx, agentName, task)
			}
			return "", fmt.Errorf("team_create: SpawnFunc not configured on reactor")
		})},
	}
	for _, bt := range remainingTools {
		if !setup.skipTools[bt.name] {
			if err := r.RegisterTool(bt.tool); err != nil {
				r.getLogger().Warn("failed to register bundled tool", "name", bt.name, "error", err)
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
	r.agentRegistry = setup.agentRegistry
	r.runtimeDir = setup.runtimeDir
	r.modelRegistry = setup.modelRegistry
	r.auditLogger = setup.auditLogger

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
	r.registerOrchestrationTools()
	r.initInteractionHandler()
	r.initToolExecutor(setup)
	r.registerBundledTools(setup)

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

	// Inject logger into offload package for proper dependency injection
	SetOffloadLogger(r.getLogger())

	// Start background cleanup for offloaded files
	CleanupOffloadedFiles()

	return r
}

func (r *Reactor) AuditLogger() core.AuditLogger { return r.auditLogger }

func (r *Reactor) logAudit(ctx context.Context, entry core.AuditEntry) {
	if r.auditLogger == nil {
		return
	}
	if err := r.auditLogger.Log(ctx, entry); err != nil {
		r.getLogger().Warn("failed to write audit log", "error", err)
	}
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
func (r *Reactor) RegisterTool(tool core.FuncTool) error {
	r.cacheMu.Lock()
	r.cachedLLMTools = nil // invalidate cache
	r.cacheMu.Unlock()
	return r.toolRegistry.Register(tool)
}
func (r *Reactor) maxHistoryTurns() int { return maxHistoryTurnsForConfig(r.config.MaxTokens) }

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
// Shared (same reference as parent):
//   - toolRegistry, skillRegistry (tool/skill definitions)
//   - toolExecutor (permission chain, hooks, result limits)
//   - memory, eventBus (persistence & events)
//   - llmCaller fields: tokenEstimator, sessionStore, mockLLM
//
// Independent (new instances for child):
//   - config (Model, SystemPrompt, Temperature, etc. — can override)
//   - taskManager (child's own task tracking)
//   - llmCaller (child's own LLM caller with independent context window)
//   - contextWindow (child's own conversation history)
//   - pendingTasks (child's own async task channels)
//   - askUser, askPermission (child's own interaction tools)
//
// Use case: SubAgent creation where the child needs parent's tools/skills/memory
// but runs its own T-A-O loop with possibly a different model or system prompt.
func (r *Reactor) CloneReactor(configOverride ReactorConfig) *Reactor {
	childConfig := r.config
	if configOverride.Model != "" {
		childConfig.Model = configOverride.Model
	}
	// FIX(P0-Safe): CloneReactor creates an independent Reactor for a DIFFERENT Agent
	// with its own identity (role, rules, constraints). The new Agent will call
	// Reload(AgentConfig) to inject its own complete SystemPrompt.
	// Therefore, NEVER inherit or append the parent's SystemPrompt — always use
	// the override value directly, or clear to prevent identity leakage.
	if configOverride.SystemPrompt != "" {
		childConfig.SystemPrompt = configOverride.SystemPrompt
	} else {
		childConfig.SystemPrompt = ""
	}
	if configOverride.APIKey != "" {
		childConfig.APIKey = configOverride.APIKey
	}
	if configOverride.BaseURL != "" {
		childConfig.BaseURL = configOverride.BaseURL
	}
	if configOverride.AuthToken != "" {
		childConfig.AuthToken = configOverride.AuthToken
	}
	if configOverride.Temperature > 0 {
		childConfig.Temperature = configOverride.Temperature
	}
	if configOverride.TopP > 0 {
		childConfig.TopP = configOverride.TopP
	}
	if configOverride.TopK > 0 {
		childConfig.TopK = configOverride.TopK
	}
	if configOverride.PresencePenalty != 0 {
		childConfig.PresencePenalty = configOverride.PresencePenalty
	}
	if configOverride.FrequencyPenalty != 0 {
		childConfig.FrequencyPenalty = configOverride.FrequencyPenalty
	}
	if configOverride.MaxTokens > 0 {
		childConfig.MaxTokens = configOverride.MaxTokens
	}
	if configOverride.IsLocal {
		childConfig.IsLocal = configOverride.IsLocal
	}

	child := &Reactor{
		config:        childConfig,
		toolRegistry:  r.toolRegistry,
		toolExecutor:  r.toolExecutor,
		skillRegistry: r.skillRegistry,
		ruleRegistry:  r.ruleRegistry,
		memory:        r.memory,
		eventBus:      r.eventBus,
		agentRegistry: r.agentRegistry,
		runtimeDir:    r.runtimeDir,
		modelRegistry: r.modelRegistry,
		kvStore:       r.kvStore,
		fileStore:     r.fileStore,
	}

	// Clone LLMCaller with parent's shared infrastructure but independent client/context
	child.llmCaller = r.cloneLLMCallerForChild(childConfig)

	child.interactionHandler = NewDefaultInteractionHandler(func(e core.ReactEvent) {
		if child.eventBus != nil {
			child.eventBus.Emit(e)
		}
	})

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

	return r.runLoop(reactCtx, 0, time.Now())
}

func (r *Reactor) RunFromSnapshot(ctx context.Context, snapshot *RunSnapshot, newInput string) (*RunResult, error) {
	reactCtx := NewReactContextFromSnapshot(ctx, snapshot)

	if r.eventBus != nil {
		reactCtx.emitEvent = r.eventBus.Emit
	}

	reactCtx.IsTerminated = false
	reactCtx.TerminationReason = ""

	if newInput != "" {
		reactCtx.AddMessage("user", newInput)
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

	// Offload large results (>30K chars) before persisting
	r.offloadLargeResults(reactCtx)

	// Structured assistant message: thought content
	thoughtMsg := fmt.Sprintf("Thought: %s\nDecision: %s", reactCtx.LastThought.Reasoning, reactCtx.LastThought.Decision)
	reactCtx.AddMessage("assistant", thoughtMsg)
	r.persistStepToStore(reactCtx.Ctx(), "assistant", thoughtMsg)

	// Structured tool message: action result (if tool was called)
	if reactCtx.LastThought.Decision == DecisionAct {
		if reactCtx.LastAction.Error != nil {
			toolMsg := fmt.Sprintf("%s error: %s", reactCtx.LastAction.Target, reactCtx.LastAction.ErrorMsg)
			reactCtx.AddMessage("tool", toolMsg)
			r.persistStepToStore(reactCtx.Ctx(), "tool", toolMsg)
		} else if reactCtx.LastAction.Result != "" {
			toolMsg := fmt.Sprintf("%s returned: %s", reactCtx.LastAction.Target, reactCtx.LastAction.Result)
			reactCtx.AddMessage("tool", toolMsg)
			r.persistStepToStore(reactCtx.Ctx(), "tool", toolMsg)
		}
	}
}

// buildResultFromContext constructs a RunResult from the ReactContext state.
func (r *Reactor) buildResultFromContext(reactCtx *ReactContext, totalTokens int, runStart time.Time) *RunResult {
	result := &RunResult{
		Steps:             reactCtx.History,
		TotalIterations:   reactCtx.CurrentIteration,
		TerminationReason: reactCtx.TerminationReason,
		TokensUsed:        totalTokens,
	}

	if reactCtx.LastAction != nil {
		result.Answer = reactCtx.LastAction.Result
	}
	if result.Answer == "" && reactCtx.LastThought != nil {
		result.Answer = reactCtx.LastThought.FinalAnswer
	}
	if result.Answer == "" && reactCtx.LastObservation != nil {
		result.Answer = reactCtx.LastObservation.Result
	}
	if result.Answer == "" && reactCtx.TerminationReason != "" {
		result.Answer = fmt.Sprintf("<task-terminated>%s</task-terminated>", reactCtx.TerminationReason)
	}

	if result.Answer != "" {
		reactCtx.EmitEvent(core.FinalAnswer, result.Answer)
	}

	totalDuration := time.Since(runStart)
	result.TotalDuration = totalDuration

	summary := core.ExecutionSummaryData{
		TotalIterations:   result.TotalIterations,
		TotalDuration:     totalDuration,
		TokensUsed:        totalTokens,
		TerminationReason: result.TerminationReason,
	}
	summary.ToolsUsed = collectUniqueToolNames(reactCtx.History)
	summary.ToolCalls = 0
	for _, step := range reactCtx.History {
		if step.Action.Type == ActionTypeToolCall && step.Action.Target != "" {
			summary.ToolCalls++
		}
	}
	reactCtx.EmitEvent(core.ExecutionSummary, summary)

	// Emit TaskSummary for non-trivial tasks (more than 1 iteration or at least 1 tool call)
	taskSummaryData := core.TaskSummaryData{
		InputTokens:  totalTokens,
		OutputTokens: 0,
	}
	if result.TotalIterations > 1 || summary.ToolCalls > 0 {
		toolWord := "tool calls"
		if summary.ToolCalls == 1 {
			toolWord = "tool call"
		}
		taskSummaryData.Summary = fmt.Sprintf(
			"Completed %d iteration(s) with %d %s in %s. Termination reason: %s.",
			result.TotalIterations, summary.ToolCalls, toolWord,
			totalDuration.Round(time.Millisecond), result.TerminationReason,
		)
	} else if result.Answer != "" {
		taskSummaryData.Summary = fmt.Sprintf("Direct answer provided. %s", result.TerminationReason)
	}
	reactCtx.EmitEvent(core.TaskSummary, taskSummaryData)

	return result
}

// handlePauseSnapshot checks if a pause was requested and saves a snapshot if so.
func (r *Reactor) handlePauseSnapshot(reactCtx *ReactContext) {
	r.pauseMu.Lock()
	paused := r.pauseRequested
	r.pauseRequested = false
	r.pauseMu.Unlock()
	if paused {
		snap := reactCtx.ToSnapshot()
		snap.TerminationReason = "paused"
		r.setSnapshot(snap)
	}
}

func (r *Reactor) runLoop(reactCtx *ReactContext, initialTokens int, runStart time.Time) (*RunResult, error) {
	totalTokens := initialTokens
	sessionID := r.resolveSessionID(reactCtx)
	r.getLogger().Info("run loop start",
		"session_id", sessionID,
		"max_iterations", reactCtx.MaxIterations,
		"input_preview", truncate(reactCtx.Input, 80),
	)

	for reactCtx.CurrentIteration < reactCtx.MaxIterations {
		if terminated, reason := r.CheckTermination(reactCtx); terminated {
			reactCtx.IsTerminated = true
			reactCtx.TerminationReason = reason
			r.getLogger().Info("run loop terminated",
				"session_id", sessionID,
				"iteration", reactCtx.CurrentIteration+1,
				"reason", reason,
			)
			break
		}

		cycleStart := time.Now()
		cycleNum := reactCtx.CurrentIteration + 1
		r.getLogger().Info("cycle start",
			"session_id", sessionID,
			"iteration", cycleNum,
		)
		r.toolExecutor.ResetCycle()

		tokens, err := r.Think(reactCtx)
		totalTokens += tokens
		reactCtx.CurrentInputTokens = tokens
		if err != nil {
			reactCtx.TerminationReason = fmt.Sprintf("think error: %v", err)
			reactCtx.EmitEvent(core.Error, reactCtx.TerminationReason)
			r.getLogger().Error("cycle abort", err,
				"session_id", sessionID,
				"iteration", cycleNum,
				"phase", "think",
				"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			)
			break
		}
		reactCtx.EmitEvent(core.ThinkingDone, reactCtx.LastThought)

		// ====== Coordinator Mode: Skip normal Act/Observe, use coord path ======
		if reactCtx.Mode == ModeCoordinator && reactCtx.LastThought != nil &&
			reactCtx.LastThought.Decision == DecisionCoordinate {
			return r.runCoordinatorLoop(reactCtx, totalTokens, cycleStart, runStart)
		}

		if err := r.Act(reactCtx); err != nil {
			reactCtx.TerminationReason = fmt.Sprintf("act error: %v", err)
			reactCtx.EmitEvent(core.Error, reactCtx.TerminationReason)
			r.getLogger().Error("cycle abort", err,
				"session_id", sessionID,
				"iteration", cycleNum,
				"phase", "act",
				"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			)
			break
		}

		if reactCtx.LastAction.Type == ActionTypeToolCall {
			r.emitActionResult(reactCtx)
		}

		if err := r.Observe(reactCtx); err != nil {
			reactCtx.TerminationReason = fmt.Sprintf("observe error: %v", err)
			reactCtx.EmitEvent(core.Error, reactCtx.TerminationReason)
			r.getLogger().Error("cycle abort", err,
				"session_id", sessionID,
				"iteration", cycleNum,
				"phase", "observe",
				"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			)
			break
		}
		reactCtx.EmitEvent(core.ObservationDone, reactCtx.LastObservation)

		r.persistStep(reactCtx, cycleStart)
		reactCtx.CurrentIteration++

		r.getLogger().Info("cycle end",
			"session_id", sessionID,
			"iteration", cycleNum,
			"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			"input_tokens", tokens,
		)
	}

	result := r.buildResultFromContext(reactCtx, totalTokens, runStart)
	r.getLogger().Info("run loop done",
		"session_id", sessionID,
		"total_iterations", result.TotalIterations,
		"total_tokens", totalTokens,
		"total_elapsed_ms", time.Since(runStart).Milliseconds(),
		"termination_reason", result.TerminationReason,
	)
	r.handlePauseSnapshot(reactCtx)

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

// emitActionResult emits ActionStart and ActionResult events for a tool call action.
func (r *Reactor) emitActionResult(reactCtx *ReactContext) {
	predictedTokens := reactCtx.CurrentInputTokens
	if predictedTokens > 0 {
		predictedTokens = int(float64(predictedTokens) * 1.5)
	}

	reactCtx.EmitEvent(core.ActionStart, core.ActionStartData{
		ToolName:        reactCtx.LastAction.Target,
		Params:          reactCtx.LastAction.Params,
		PredictedTokens: predictedTokens,
		Iteration:       reactCtx.CurrentIteration,
	})
	resultData := core.ActionResultData{
		ToolName: reactCtx.LastAction.Target,
		Duration: reactCtx.LastAction.Duration,
		Success:  reactCtx.LastAction.Error == nil,
	}
	if reactCtx.LastAction.Error != nil {
		resultData.Error = reactCtx.LastAction.ErrorMsg
	} else {
		resultData.Result = reactCtx.LastAction.Result
	}
	reactCtx.EmitEvent(core.ActionResult, resultData)
}

// runCoordinatorLoop runs the Coordinator-mode T-A-O loop (Design §4.3 / §10).
// When Think produces DecisionCoordinate, this method takes over:
//
// 1. Act: Reports coordination status
// 2. Observe: Checks sub-task completion, produces summary when all done
// 3. Loops with polling interval until all tasks complete or timeout/cancel
//
// This is separate from runLoop because Coordinator mode has fundamentally
// different control flow — it doesn't call tools, it waits for async results.
func (r *Reactor) runCoordinatorLoop(reactCtx *ReactContext, totalTokens int, coordStart, runStart time.Time) (*RunResult, error) {
	cs := reactCtx.CoordState
	if cs == nil {
		return nil, fmt.Errorf("coordinator mode but no CoordState")
	}

	r.getLogger().Info("entering coordinator wait loop", "parent", cs.ParentTaskID,
		"tasks", cs.TaskProgress.Count())

	// Poll interval: start at 500ms, adaptive up to 5s
	pollInterval := 500 * time.Millisecond
	maxPollInterval := 5 * time.Second
	coordDeadline := time.Now().Add(10 * time.Minute) // Default coordinator timeout

	for reactCtx.CurrentIteration < reactCtx.MaxIterations {
		if terminated, reason := r.CheckTermination(reactCtx); terminated {
			reactCtx.IsTerminated = true
			reactCtx.TerminationReason = reason
			break
		}

		// Check lifecycle state
		if cs.LifecycleState.IsTerminal() {
			break
		}

		// Check global deadline
		if time.Now().After(coordDeadline) {
			cs.Cancel("coordinator global timeout exceeded")
			break
		}

		cycleStart := time.Now()

		// Act (coordination status report)
		if err := r.Act(reactCtx); err != nil {
			r.getLogger().Warn("coordinator act error", "error", err)
		}
		reactCtx.EmitEvent(core.ThinkingDone, reactCtx.LastThought)

		// Observe (check task completion)
		if err := r.Observe(reactCtx); err != nil {
			r.getLogger().Warn("coordinator observe error", "error", err)
			break
		}
		reactCtx.EmitEvent(core.ObservationDone, reactCtx.LastObservation)

		// Record step in history (for debugging/audit)
		step := Step{
			Iteration:   reactCtx.CurrentIteration + 1,
			Thought:     *reactCtx.LastThought,
			Action:      *reactCtx.LastAction,
			Observation: *reactCtx.LastObservation,
			Timestamp:   time.Now(),
			Duration:    time.Since(cycleStart),
		}
		reactCtx.AppendHistory(step)
		reactCtx.CurrentIteration++

		// If Observe produced a final answer (all tasks done), exit loop
		if reactCtx.LastThought != nil && reactCtx.LastThought.Decision == DecisionAnswer &&
			reactCtx.LastThought.IsFinal {
			r.getLogger().Info("coordinator loop complete", "iterations", reactCtx.CurrentIteration)
			break
		}

		// Adaptive poll: increase interval if no new results
		pending := cs.TaskProgress.PendingCount()
		if pending > 0 && pollInterval < maxPollInterval {
			pollInterval *= 2
			if pollInterval > maxPollInterval {
				pollInterval = maxPollInterval
			}
		}

		// Wait before next poll cycle
		select {
		case <-time.After(pollInterval):
			// Normal poll tick
		case <-reactCtx.Ctx().Done():
			// External context cancelled
			cs.Cancel("external context cancelled")
			break
		case ctrl := <-cs.ControlChan:
			// Lifecycle control command received
			r.handleCoordinatorControl(cs, ctrl)
		}

		if cs.LifecycleState.IsTerminal() {
			break
		}
	}

	// Build result from coordinator state
	result := &RunResult{
		Steps:             reactCtx.History,
		TotalIterations:   reactCtx.CurrentIteration,
		TerminationReason: reactCtx.TerminationReason,
		TokensUsed:        totalTokens,
		TotalDuration:     time.Since(runStart),
	}

	// Extract final answer from last thought or observation
	if reactCtx.LastThought != nil && reactCtx.LastThought.FinalAnswer != "" {
		result.Answer = reactCtx.LastThought.FinalAnswer
	} else if reactCtx.LastObservation != nil {
		result.Answer = reactCtx.LastObservation.Result
	}
	if result.Answer == "" && cs.TaskProgress != nil {
		result.Answer = cs.TaskProgress.Summary()
	}
	if reactCtx.TerminationReason == "" {
		reactCtx.TerminationReason = "coordination_complete"
	}

	// Cleanup coordinator resources
	cs.Dispose()
	reactCtx.Mode = ModeExecutor // Reset to executor mode

	return result, nil
}

// handleCoordinatorControl processes an incoming lifecycle control command.
func (r *Reactor) handleCoordinatorControl(cs *CoordState, cmd *core.ControlCommand) {
	switch cmd.Action {
	case core.CmdInterrupt:
		if err := cs.Interrupt(cmd.Reason); err != nil {
			r.getLogger().Warn("coordinator interrupt failed", "error", err)
		}
	case core.CmdCancel:
		if err := cs.Cancel(cmd.Reason); err != nil {
			r.getLogger().Warn("coordinator cancel failed", "error", err)
		}
	default:
		r.getLogger().Info("unknown coordinator control command", "action", cmd.Action)
	}
}

// wrapError wraps an error with context information.
func wrapError(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

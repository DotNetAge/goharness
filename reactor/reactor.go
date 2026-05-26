package reactor

import (
	"context"
	"encoding/json"
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
	ContextLength    int
	MaxTokens        int

	SystemPrompt  string
	MaxIterations int

	Logger core.Logger // Unified logging interface (optional, defaults to slog)

	IsLocal          bool
	AsyncToolTimeout time.Duration // Timeout for async tool execution (default 5 minutes)
	SyncToolTimeout  time.Duration // Timeout for sync tool execution (default 5 minutes)
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
	if override.ContextLength > 0 {
		c.ContextLength = override.ContextLength
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
	TokenUsage        core.TokenUsage `json:"token_usage,omitempty" yaml:"token_usage,omitempty"`
	TotalDuration     time.Duration `json:"total_duration_ms,omitempty" yaml:"total_duration_ms,omitempty"`
}

// Runner is the public interface for the reactor loop.
type Runner interface {
	Run(ctx context.Context, input string, history ConversationHistory) (*RunResult, error)
}

var _ Runner = (*Reactor)(nil)

// Reactor is the core agent execution engine.
// It implements the Runner, RegistryHub, SessionManager, and ThinkExecutor interfaces.
//
// Reactor orchestrates the LLM-driven agent loop:
//  1. Think: Call LLM with context → derive Thought from native response
//  2. Act: Execute tool calls or use the raw answer
//  3. Repeat until LLM answers directly or termination conditions are met
//
// Thread Safety:
//   - Public methods are safe for concurrent use where documented
//   - Internal state uses RWMutex for cached data (cachedLLMTools)
//   - Hook lists are immutable after construction (sort once at setup)
//
// Lifecycle:
//
//	Created via NewReactor() with ReactorConfig and ReactorOptions.
//	Use Run() to execute a single user request through the agent loop.
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
	// Long-term/project knowledge — used by syncProjectMemory and external consumers.
	memory core.Memory

	// sessionMemory is a separate short-term memory instance for the memory closed loop.
	// When set, MemoryThoughtHook and MemorySlideHandler use this instead of memory.
	// Typically backed by a session-scoped RAG index (SessionRAG).
	sessionMemory core.Memory

	// llmCaller handles all LLM API interactions including streaming and token management.
	llmCaller *LLMCaller

	// prompt builds the structured system prompt from multiple sections.
	prompt *Prompt

	// eventBus publishes ReactEvent messages to external subscribers (UI, loggers, etc.).
	eventBus EventBus

	// resultStore accumulates tool execution results for cross-reference.
	resultStore *core.ResultStore

	// kvStore provides session-scoped key-value storage for tool state sharing.
	kvStore core.KVStore

	// fileStore provides session-scoped file storage for temp files and drafts.
	fileStore core.FileStore

	// SpawnFunc creates sub-agents for the SubAgent tool.
	// Set by Agent after Reactor creation to avoid circular deps.
	SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

	// AgentTalkFunc sends a message to another agent in a specific session and returns the reply.
	// Unlike SpawnFunc (one-shot delegation), AgentTalk maintains a conversation thread
	// via sessionID — the target agent sees the full history when the same ID is reused.
	// Set by Agent after Reactor creation to avoid circular deps.
	AgentTalkFunc func(ctx context.Context, to, sessionID, message string) (string, error)

	// cachedLLMTools caches the LLM-ready tool definitions.
	// The full tool registry is converted once after all tools are registered
	// and reused across Think-Act cycles to avoid per-round conversion overhead.
	// Invalidated when RegisterTool is called after construction.
	cachedLLMTools []gochatcore.Tool
	cacheMu        sync.RWMutex

	// Directory context (Design-time safety: set during initialization from setup)
	projectDir string // Layer 2: Project working directory (always non-empty after init)
	sessionDir string // Layer 3: Session working directory


	// asyncToolTimeout is the timeout for async tool execution (default 5 minutes)
	asyncToolTimeout time.Duration
	// syncToolTimeout is the timeout for sync tool execution (default 5 minutes)
	syncToolTimeout time.Duration

	// Hook 集合 — 构建时按 priority 排序，运行时只读
	thoughtHooks     []ThoughtHook
	toolHooks        []ToolHook

	// stuckAnalyzer 检测迭代卡死模式，注入提示或终止循环。
	stuckAnalyzer    StuckDetector
	stuckMu          sync.Mutex
	stuckNudgeCounts map[StuckPattern]int
}

// EventBus returns the event bus for subscribing to reactor events.
func (r *Reactor) EventBus() EventBus { return r.eventBus }

// Memory returns the memory instance for knowledge retrieval (may be nil if not configured).
func (r *Reactor) Memory() core.Memory { return r.memory }

// Prompt returns the prompt builder for system prompt generation.
func (r *Reactor) Prompt() *Prompt { return r.prompt }

// Logger returns the reactor's logger instance.
func (r *Reactor) Logger() core.Logger { return r.getLogger() }


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
		tr.Metadata = res.Metadata
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
	eventBus       EventBus
	skillDirs      []string
	skills         []string
	memory         core.Memory
	sessionMemory  core.Memory
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

	// Hook 注入
	thoughtHooks     []ThoughtHook
	toolHooks        []ToolHook

	stuckAnalyzer StuckDetector
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
	// Wire logger into EventBus for observability (subscribe/emit/drop tracing)
	if bus, ok := r.eventBus.(*InProcessEventBus); ok {
		bus.SetLogger(r.getLogger())
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
		gochat.WithTimeout(4*time.Minute),
	)

	var llmOpts []LLMCallerOption
	if setup.sessionStore != nil {
		llmOpts = append(llmOpts, WithLLMCallerSessionStore(setup.sessionStore))
	}
	if setup.mockLLM != nil {
		llmOpts = append(llmOpts, WithLLMCallerMock(setup.mockLLM))
	}
	// Auto-wire memory slide handler (write-half of memory closed loop).
	// Uses sessionMemory when available (preferred for slid-context storage),
	// falling back to the primary memory.
	memForSlide := setup.sessionMemory
	if memForSlide == nil {
		memForSlide = setup.memory
	}
	if memForSlide != nil {
		llmOpts = append(llmOpts, WithLLMCallerSlideHandler(core.NewMemorySlideHandler(memForSlide)))
	}

	r.llmCaller = NewLLMCaller(llmCfg, client, setup.sessionStore, llmOpts...)
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
		core.WithPermissionChecker(tools.NewFallbackPermissionChecker()),
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
	factory  func() core.FuncTool
	skipName string
}

var defaultBundledTools = []toolRegistration{
	{"Grep", func() core.FuncTool { return tools.NewGrepTool() }, "Grep"},
	{"Glob", func() core.FuncTool { return tools.NewGlobTool() }, "Glob"},
	{"Read", func() core.FuncTool { return tools.NewReadTool() }, "Read"},
	{"Write", func() core.FuncTool { return tools.NewWriteTool() }, "Write"},
	{"FileEdit", func() core.FuncTool { return tools.NewFileEditTool() }, "FileEdit"},
	{"Bash", func() core.FuncTool { return tools.NewBashTool() }, "Bash"},
	{"RunScript", func() core.FuncTool { return tools.NewRunScriptTool() }, "RunScript"},
	{"PowerShell", func() core.FuncTool {
		if !tools.IsWindowsPlatform() {
			return nil
		}
		return tools.NewPowerShellTool()
	}, "PowerShell"},
	{"WebSearch", func() core.FuncTool { return tools.NewWebSearchTool() }, "WebSearch"},
	{"WebFetch", func() core.FuncTool { return tools.NewWebFetchTool() }, "WebFetch"},
	{"AskUser", func() core.FuncTool { return tools.NewAskUserTool() }, "AskUser"},
	{"Ls", func() core.FuncTool { return tools.NewLsTool() }, "Ls"},
	{"CollectResults", func() core.FuncTool { return tools.NewCollectResultsTool() }, "CollectResults"},
	{"TaskList", func() core.FuncTool { return tools.NewTaskListTool() }, "TaskList"},
	{"TaskGet", func() core.FuncTool { return tools.NewTaskGetTool() }, "TaskGet"},
	{"TaskUpdate", func() core.FuncTool { return tools.NewTaskUpdateTool() }, "TaskUpdate"},

}

func (r *Reactor) registerBundledTools(setup *reactorSetup) {
	if setup.skipAllBundled {
		return
	}

	for _, reg := range defaultBundledTools {
		if setup.skipTools[reg.skipName] {
			continue
		}
		tool := reg.factory()
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
	orchestrationTools := []struct {
		name string
		tool core.FuncTool
	}{
		{"SubAgent", tools.NewSubAgentTool(func(ctx context.Context, agentName, task string) (string, error) {
			if r.SpawnFunc != nil {
				return r.SpawnFunc(ctx, agentName, task)
			}
			return "", fmt.Errorf("subagent: SpawnFunc not configured on reactor")
		})},
		{"AgentTalk", tools.NewAgentTalkTool(func(ctx context.Context, to, sessionID, message string) (string, error) {
			if r.AgentTalkFunc != nil {
				return r.AgentTalkFunc(ctx, to, sessionID, message)
			}
			return "", fmt.Errorf("agent_talk: AgentTalkFunc not configured on reactor")
		})},
		{"TaskCreate", tools.NewTaskCreateTool()},
		{"TeamGetTasks", tools.NewTeamGetTasksTool()},
		{"TeamList", tools.NewTeamListTool()},
		{"TeamDelete", tools.NewTeamDeleteTool()},
		{"TeamCreate", tools.NewTeamCreateTool(func(ctx context.Context, agentName, task string) (string, error) {
			if r.SpawnFunc != nil {
				return r.SpawnFunc(ctx, agentName, task)
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
	r.sessionMemory = setup.sessionMemory
	r.prompt = setup.prompt
	// Initialize async tool timeout (default 5 minutes if not configured)
	if config.AsyncToolTimeout > 0 {
		r.asyncToolTimeout = config.AsyncToolTimeout
	} else {
		r.asyncToolTimeout = 5 * time.Minute
	}
	// Initialize sync tool timeout (default 5 minutes if not configured)
	if config.SyncToolTimeout > 0 {
		r.syncToolTimeout = config.SyncToolTimeout
	} else {
		r.syncToolTimeout = 5 * time.Minute
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

	// ── Hook 初始化 ──
	// 用户 hooks — 通过 Option 追加
	r.thoughtHooks = append(r.thoughtHooks, setup.thoughtHooks...)
	r.toolHooks = append(r.toolHooks, setup.toolHooks...)

	// Auto-register MemoryThoughtHook (read-half of memory closed loop).
	// Retrieves relevant context from Memory and injects into System Prompt
	// before each Think phase. Registered as a built-in hook at priority 50.
	// Auto-register MemoryThoughtHook (read-half of memory closed loop).
	// Uses sessionMemory when available, falling back to primary memory.
	memForHook := setup.sessionMemory
	if memForHook == nil {
		memForHook = setup.memory
	}
	if memForHook != nil {
		r.thoughtHooks = append(r.thoughtHooks, NewMemoryThoughtHook(memForHook))
	}

	// 按 priority 排序（per-chain）
	sortByPriority(r.thoughtHooks)
	sortByPriority(r.toolHooks)

	// StuckDetector
	if setup.stuckAnalyzer != nil {
		r.stuckAnalyzer = setup.stuckAnalyzer
		r.stuckNudgeCounts = make(map[StuckPattern]int)
	}

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
		gochat.WithTimeout(4*time.Minute),
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
// memory, hooks, and the event bus from the parent, but has its own independent:
//   - Config (model, API key, temperature, etc.)
//   - LLM caller (client, context window, token usage tracking)
//   - asyncToolTimeout (copied from parent config, can be overridden)
//
// Use case: SubAgent creation where the child needs parent's tools/skills/memory
// but runs its own Think-Act loop with possibly a different model or system prompt.
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
		sessionMemory:            r.sessionMemory,
		eventBus:                 r.eventBus,
		kvStore:                  r.kvStore,
		fileStore:                r.fileStore,
		thoughtHooks:             r.thoughtHooks,
		toolHooks:                r.toolHooks,
		stuckAnalyzer:            r.stuckAnalyzer,
		stuckNudgeCounts:         r.stuckNudgeCounts,
		asyncToolTimeout:         r.asyncToolTimeout,
	}

	// Clone LLMCaller with parent's shared infrastructure but independent client/context
	child.llmCaller = r.cloneLLMCallerForChild(childConfig)

	return child
}

// cloneLLMCallerForChild creates a new LLMCaller for CloneReactor,
// sharing the parent's infrastructure (sessionStore, mockLLM)
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
		gochat.WithTimeout(4*time.Minute),
	)

	parentCaller := r.llmCaller
	var llmOpts []LLMCallerOption
	if parentCaller != nil {
		if parentCaller.SessionStore() != nil {
			llmOpts = append(llmOpts, WithLLMCallerSessionStore(parentCaller.SessionStore()))
		}
		return NewLLMCaller(llmCfg, client, parentCaller.SessionStore(), llmOpts...)
	}

	// Fallback: create standalone LLMCaller for child without parent infrastructure
	return NewLLMCaller(llmCfg, client, nil, llmOpts...)
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

// persistStep records a cycle step into history and persistent storage.
// The assistant message preserves the raw LLM content verbatim, with
// native tool calls attached. This ensures the LLM sees its own exact
// output in subsequent iterations, improving coherence.
//
// Messages stored:
//
//	assistant: <raw LLM content>  (+ ToolCalls if tools were used)
//	tool:      "<tool_name> returned: <result>"     (if tool was called)
//	tool:      "<tool_name> error: <error>"           (if tool errored)
func (r *Reactor) persistStep(reactCtx *ReactContext, cycleStart time.Time) {
	if reactCtx.LastThought == nil || reactCtx.LastAction == nil {
		r.getLogger().Warn("persistStep: skipped — LastThought or LastAction is nil",
			"session_id", r.resolveSessionID(reactCtx),
			"iteration", reactCtx.CurrentIteration,
		)
		return
	}

	step := Step{
		Iteration: reactCtx.CurrentIteration + 1,
		Thought:   *reactCtx.LastThought,
		Action:    *reactCtx.LastAction,
		Timestamp: time.Now(),
		Duration:  time.Since(cycleStart),
	}
	reactCtx.AppendHistory(step)

	reactCtx.EmitEvent(core.CycleEnd, core.CycleInfo{
		Iteration: reactCtx.CurrentIteration + 1,
		Duration:  time.Since(cycleStart),
	})

	// Assistant message: raw LLM content with native tool calls
	var toolCalls []core.ToolCall
	if reactCtx.LastThought.Decision == DecisionAct && len(reactCtx.LastThought.ToolCallList) > 0 {
		toolCalls = make([]core.ToolCall, len(reactCtx.LastThought.ToolCallList))
		for i, tc := range reactCtx.LastThought.ToolCallList {
			argsJSON, _ := json.Marshal(tc.Arguments)
			toolCalls[i] = core.ToolCall{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: string(argsJSON),
			}
		}
	}
	reactCtx.AddMessage("assistant", reactCtx.LastThought.Content, toolCalls...)
	assistantMsg := core.Message{
		Role:      "assistant",
		Content:   reactCtx.LastThought.Content,
		Timestamp: time.Now().Unix(),
		ToolCalls: toolCalls,
	}
	r.persistStepToStore(reactCtx.Ctx(), assistantMsg)

	// Structured tool messages: one per tool result (with correct tool_call_id)
	if reactCtx.LastThought.Decision == DecisionAct {
		for _, tr := range reactCtx.LastAction.Results {
			toolCallID := tr.ToolCallID
			if toolCallID == "" {
				toolCallID = lookUpToolCallID(reactCtx.LastThought, tr.ToolName)
			}
			toolMsg := tr.ToolResultSummary()
			reactCtx.AddToolMessage("tool", toolMsg, toolCallID)
			r.persistStepToStore(reactCtx.Ctx(), core.Message{
				Role:       "tool",
				Content:    toolMsg,
				Timestamp:  time.Now().Unix(),
				ToolCallID: toolCallID,
			})
		}
	}
}

// buildResultFromContext constructs a RunResult from the ReactContext state.
func (r *Reactor) buildResultFromContext(reactCtx *ReactContext, aggregated core.TokenUsage, runStart time.Time) *RunResult {
	answer := extractAnswer(reactCtx)
	if answer != "" {
		reactCtx.EmitEvent(core.FinalAnswer, answer)
	}

	totalDuration := time.Since(runStart)

	summary := buildExecutionSummary(reactCtx, aggregated, totalDuration)
	reactCtx.EmitEvent(core.ExecutionSummary, summary)

	taskSummary := buildTaskSummary(answer, aggregated, reactCtx.CurrentIteration,
		summary.ToolCalls, totalDuration, reactCtx.TerminationReason)
	reactCtx.EmitEvent(core.TaskSummary, taskSummary)

	return &RunResult{
		Answer:            answer,
		Steps:             reactCtx.History,
		TotalIterations:   reactCtx.CurrentIteration,
		TerminationReason: reactCtx.TerminationReason,
		TokenUsage:        aggregated,
		TotalDuration:     totalDuration,
	}
}

func extractAnswer(reactCtx *ReactContext) string {
	// DecisionAnswer: the raw LLM Content is the answer (via ToolResult{Result: content})
	if reactCtx.LastAction != nil && len(reactCtx.LastAction.Results) > 0 {
		for _, r := range reactCtx.LastAction.Results {
			if r.ToolName == "answer" {
				return r.Result
			}
		}
		return reactCtx.LastAction.Summary()
	}
	// Fallback: use raw content from Thought
	if reactCtx.LastThought != nil && reactCtx.LastThought.Content != "" {
		return reactCtx.LastThought.Content
	}
	if reactCtx.TerminationReason != "" {
		return fmt.Sprintf("<task-terminated>%s</task-terminated>", reactCtx.TerminationReason)
	}
	return ""
}

func buildExecutionSummary(reactCtx *ReactContext, aggregated core.TokenUsage, totalDuration time.Duration) core.ExecutionSummaryData {
	summary := core.ExecutionSummaryData{
		TotalIterations:   reactCtx.CurrentIteration,
		TotalDuration:     totalDuration,
		TokensUsed:        aggregated,
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

func buildTaskSummary(answer string, aggregated core.TokenUsage, iterations int, toolCalls int, duration time.Duration, terminationReason string) core.TaskSummaryData {
	data := core.TaskSummaryData{
		TokenUsage: aggregated,
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

// buildIterationHistory 将 ReactContext.History 转换为 stuck detector 可分析的快照列表。
func (r *Reactor) buildIterationHistory(reactCtx *ReactContext) []IterationSnapshot {
	history := make([]IterationSnapshot, len(reactCtx.History))
	for i, step := range reactCtx.History {
		snap := IterationSnapshot{
			Iteration: step.Iteration,
			Decision:  step.Thought.Decision,
		}
		if step.Thought.Decision == DecisionAct {
			for _, tr := range step.Action.Results {
				snap.ToolCalls = append(snap.ToolCalls, ToolCallSnapshot{
					Name:  tr.ToolName,
					Error: tr.Error,
				})
			}
		}
		history[i] = snap
	}
	return history
}

// analyzeStuckPatterns 对历史迭代执行 stuck detector 分析。
// 返回诊断结果，调用方根据 nudgeCount 决定提示或终止。
func (r *Reactor) analyzeStuckPatterns(reactCtx *ReactContext) *StuckDiagnosis {
	if r.stuckAnalyzer == nil {
		return nil
	}
	history := r.buildIterationHistory(reactCtx)
	return r.stuckAnalyzer.Analyze(history)
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

	totalTokenUsage := core.TokenUsage{
		InputTokens:  initialTokens,
		OutputTokens: 0,
		TotalTokens:  initialTokens,
	}
	sessionID := r.resolveSessionID(reactCtx)
	r.getLogger().Info("run loop start",
		"session_id", sessionID,
		"max_iterations", reactCtx.MaxIterations,
		"input_preview", Truncate(reactCtx.Input, 80),
	)

	for reactCtx.CurrentIteration < reactCtx.MaxIterations {
		// 上下文取消检查 — 防止取消后继续执行 Think/Act
		if err := reactCtx.Ctx().Err(); err != nil {
			reactCtx.TerminationReason = fmt.Sprintf("context_cancelled: %v", err)
			reactCtx.IsTerminated = true
			r.getLogger().Info("run loop terminated — context cancelled",
				"session_id", sessionID,
				"iteration", reactCtx.CurrentIteration+1,
				"error", err,
			)
			break
		}

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

		cycleUsage, err := r.Think(reactCtx)
		totalTokenUsage.InputTokens += cycleUsage.InputTokens
		totalTokenUsage.OutputTokens += cycleUsage.OutputTokens
		totalTokenUsage.TotalTokens += cycleUsage.TotalTokens
		totalTokenUsage.CachedTokens += cycleUsage.CachedTokens
		totalTokenUsage.ReasoningTokens += cycleUsage.ReasoningTokens
		totalTokenUsage.AudioTokensInput += cycleUsage.AudioTokensInput
		totalTokenUsage.AudioTokensOutput += cycleUsage.AudioTokensOutput
		totalTokenUsage.AcceptedPredictionTokens += cycleUsage.AcceptedPredictionTokens
		totalTokenUsage.RejectedPredictionTokens += cycleUsage.RejectedPredictionTokens
		reactCtx.CurrentInputTokens = cycleUsage.InputTokens
		if err != nil {
			r.abortCycle(reactCtx, "think", err, cycleStart, cycleNum, sessionID)
			break
		}
		// PreCheckHook/ConvergenceHook 中止 → 跳过 Act
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

		r.persistStep(reactCtx, cycleStart)
		reactCtx.CurrentIteration++

		// LLM 没有调用工具 → 直接回答了 → 自然终止
		// 判断依据是 LLM 原生的 finish_reason 信号（与 OpenCode/ClaudeCode 一致）：
		//   "end_turn"/"stop"       → LLM 主动回答
		//   "max_tokens"/"length"   → 达到 token 上限截断
		//   无 finish_reason        → 降级检查 len(ToolCalls) == 0（兼容旧 provider）
		if reactCtx.LastThought != nil {
			fr := reactCtx.LastThought.FinishReason
			isToolUse := fr == "tool_use" || fr == "tool_calls"
			if !isToolUse && len(reactCtx.LastThought.ToolCalls) == 0 {
				reason := "direct_answer"
				switch fr {
				case "max_tokens", "length":
					reason = "max_tokens"
				}
				reactCtx.TerminationReason = reason
				reactCtx.IsTerminated = true
				r.getLogger().Info("run loop terminated",
					"session_id", sessionID,
					"iteration", cycleNum,
					"finish_reason", fr,
					"reason", reason,
				)
				break
			}
		}

		r.getLogger().Info("cycle end",
			"session_id", sessionID,
			"iteration", cycleNum,
			"elapsed_ms", time.Since(cycleStart).Milliseconds(),
			"input_tokens", cycleUsage.InputTokens,
		)
	}

	// 循环因 MaxIterations 自然退出 → 设置终止原因
	if reactCtx.TerminationReason == "" {
		reactCtx.TerminationReason = "max_iterations"
		reactCtx.IsTerminated = true
		r.getLogger().Info("run loop terminated — max iterations reached",
			"session_id", sessionID,
			"iterations", reactCtx.CurrentIteration,
			"max_iterations", reactCtx.MaxIterations,
		)
	}

	result := r.buildResultFromContext(reactCtx, totalTokenUsage, runStart)
	r.getLogger().Info("run loop done",
		"session_id", sessionID,
		"total_iterations", result.TotalIterations,
		"total_tokens", totalTokenUsage.TotalTokens,
		"total_elapsed_ms", time.Since(runStart).Milliseconds(),
		"termination_reason", result.TerminationReason,
	)

	return result, nil
}

// persistStepToStore persists an intermediate step message to the session store
// and tracks it in the LLMCaller's context window for token budget management.
func (r *Reactor) persistStepToStore(ctx context.Context, msg core.Message) {
	ss := r.llmCaller.SessionStore()
	cw := r.llmCaller.ContextWindow()
	if ss == nil || cw == nil {
		r.llmCaller.AddContextMessage(msg.Role, msg.Content)
		return
	}

	agentName := cw.Role
	r.llmCaller.AddContextMessage(msg.Role, msg.Content)
	if err := ss.Append(ctx, cw.SessionID, agentName, msg); err != nil {
		r.getLogger().Warn("failed to persist step to session store", "session_id", cw.SessionID, "role", msg.Role, "error", err)
	}
}

// BuildExecutionGuidelines returns guidelines for cautious action execution.
// DEPRECATED: Safety guidance is now merged into Behavioral Rules P2 (Execution Standards).
// Returns empty string to skip this section in ToSectionedMessages.
func BuildExecutionGuidelines() string {
	return ""
}

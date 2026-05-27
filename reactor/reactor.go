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
	StreamChannelBufferSize = 256
)

// ReactorConfig holds the configuration for creating a Reactor.
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

	Logger core.Logger

	IsLocal          bool
	AsyncToolTimeout time.Duration
	SyncToolTimeout  time.Duration
}

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
	if override.SystemPrompt != "" {
		c.SystemPrompt = override.SystemPrompt
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
	Answer            string          `json:"answer" yaml:"answer"`
	TotalIterations   int             `json:"total_iterations" yaml:"total_iterations"`
	TerminationReason string          `json:"termination_reason,omitempty" yaml:"termination_reason,omitempty"`
	TokenUsage        core.TokenUsage `json:"token_usage,omitempty" yaml:"token_usage,omitempty"`
	TotalDuration     time.Duration   `json:"total_duration_ms,omitempty" yaml:"total_duration_ms,omitempty"`
}

type Runner interface {
	Run(ctx context.Context, input string, history []core.Message) (*RunResult, error)
}

var _ Runner = (*Reactor)(nil)

// Reactor is the core agent execution engine implementing the ThinkingLoop (TAs).
type Reactor struct {
	config        ReactorConfig
	toolRegistry  core.ToolRegistry
	toolExecutor  core.ToolExecutor
	skillRegistry core.SkillRegistry
	ruleRegistry  core.RuleRegistry
	memory        core.Memory
	sessionMemory core.Memory
	llmCaller     *LLMCaller
	prompt        *Prompt
	eventBus      EventBus
	resultStore   *core.ResultStore
	kvStore       core.KVStore
	fileStore     core.FileStore

	SpawnFunc     func(ctx context.Context, agentName, task string) (string, error)
	AgentTalkFunc func(ctx context.Context, to, sessionID, message string) (string, error)

	cachedLLMTools []gochatcore.Tool
	cacheMu        sync.RWMutex

	projectDir string
	sessionDir string

	asyncToolTimeout time.Duration
	syncToolTimeout  time.Duration

	loopHooks []LoopHook
	toolHooks []ToolHook

	stuckAnalyzer    StuckDetector
	stuckMu          sync.Mutex
	stuckNudgeCounts map[StuckPattern]int
}

func (r *Reactor) EventBus() EventBus                      { return r.eventBus }
func (r *Reactor) Memory() core.Memory                     { return r.memory }
func (r *Reactor) Prompt() *Prompt                         { return r.prompt }
func (r *Reactor) Logger() core.Logger                     { return r.getLogger() }
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

func (r *Reactor) RegisterLoopHooks(hooks ...LoopHook) {
	r.loopHooks = append(r.loopHooks, hooks...)
	sortByPriority(r.loopHooks)
}

func (r *Reactor) RegisterToolHooks(hooks ...ToolHook) {
	r.toolHooks = append(r.toolHooks, hooks...)
	sortByPriority(r.toolHooks)
}

func sortByPriority[T interface{ Priority() int }](hooks []T) {
	sort.SliceStable(hooks, func(i, j int) bool {
		return hooks[i].Priority() < hooks[j].Priority()
	})
}

// ── Hook 执行器 ─────────────────────────────────────────────────────────────

func (r *Reactor) execLoopHooksBefore(sessionID string, iteration int, input *CallInput) (result HookResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("loop hook panic", fmt.Errorf("%v", p))
			result = HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range r.loopHooks {
		result = h.BeforeLLM(sessionID, iteration, input)
		if result.IsTerminal() {
			return result
		}
	}
	return HookResult{}
}

func (r *Reactor) execLoopHooksAfter(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) (result HookResult) {
	defer func() {
		if p := recover(); p != nil {
			r.getLogger().Error("loop hook panic", fmt.Errorf("%v", p))
			result = HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range r.loopHooks {
		result = h.AfterLLM(sessionID, iteration, resp, results)
		if result.IsTerminal() {
			r.notifyLoopAbort(sessionID, result.AbortReason)
			return result
		}
	}
	return HookResult{}
}

func (r *Reactor) notifyLoopAbort(sessionID string, reason string) {
	for i := len(r.loopHooks) - 1; i >= 0; i-- {
		r.loopHooks[i].Abort(sessionID, reason)
	}
}

func (r *Reactor) notifyToolAbort(reason string) {
	for i := len(r.toolHooks) - 1; i >= 0; i-- {
		r.toolHooks[i].Abort(reason)
	}
}

func (r *Reactor) getLogger() core.Logger {
	if r.config.Logger != nil {
		return r.config.Logger
	}
	return core.DefaultLogger()
}

// ── ThinkingLoop ────────────────────────────────────────────────────────────

// Run 执行 ThinkingLoop (TAs) 处理一次用户输入。
// 所有状态在函数内部维护，通过返回值输出。
func (r *Reactor) Run(ctx context.Context, input string, history []core.Message) (*RunResult, error) {
	sessionID := r.resolveSessionID(history)
	msgHistory := make([]core.Message, len(history))
	copy(msgHistory, history)

	var totalUsage core.TokenUsage
	start := time.Now()
	iteration := 0

	emit := r.makeEmitter(sessionID)

	for iteration < r.config.MaxIterations {
		if err := ctx.Err(); err != nil {
			return r.buildResult("", "request cancelled", totalUsage, time.Since(start), iteration), nil
		}

		callInput := r.buildCallInput(sessionID, input, msgHistory, iteration)

		resp, toolResults, usage, err := r.executeTurn(ctx, sessionID, input, iteration, msgHistory, callInput, emit)
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens

		if err != nil {
			return r.buildResult("", fmt.Sprintf("error: %v", err), totalUsage, time.Since(start), iteration), nil
		}

		r.persistMessages(sessionID, resp, toolResults, iteration, &msgHistory)
		iteration++

		emit(core.CycleEnd, core.CycleInfo{Iteration: iteration, Duration: time.Since(start)})

		if resp.AbortReason != "" {
			answer := resp.Content
			if answer == "" {
				answer = resp.Reasoning
			}
			return r.buildResult(answer, resp.AbortReason, totalUsage, time.Since(start), iteration), nil
		}

		if len(resp.ToolCalls) == 0 {
			answer := resp.Content
			if answer == "" {
				answer = resp.Reasoning
			}
			reason := "direct_answer"
			if resp.FinishReason == "max_tokens" || resp.FinishReason == "length" {
				reason = "max_tokens"
			}
			emit(core.FinalAnswer, answer)
			return r.buildResult(answer, reason, totalUsage, time.Since(start), iteration), nil
		}
	}

	return r.buildResult("", "max_iterations", totalUsage, time.Since(start), iteration), nil
}

// ── buildCallInput ──────────────────────────────────────────────────────────

func (r *Reactor) buildCallInput(sessionID, input string, history []core.Message, iteration int) *CallInput {
	llmTools := r.getLLMTools()

	sessionDir := ""
	if r.fileStore != nil {
		sessionDir = r.fileStore.GetSessionPath(sessionID)
	}
	if sessionDir == "" && r.sessionDir != "" {
		sessionDir = r.sessionDir
	}

	var sections []gochatcore.Message
	if r.prompt != nil {
		sections = r.prompt.ToSectionedMessages(sessionID, sessionDir, r.projectDir)
	}

	compactHistory := core.MicroCompact(history, 2)

	return &CallInput{
		SessionID:            sessionID,
		SystemPromptSections: sections,
		UserMessage:          input,
		History:              compactHistory,
		Tools:                llmTools,
	}
}

// ── persistMessages ─────────────────────────────────────────────────────────

func (r *Reactor) persistMessages(sessionID string, resp *LLMResponse, results []ToolResult, iteration int, history *[]core.Message) {
	var toolCalls []core.ToolCall
	for _, inv := range resp.ToolCalls {
		argsJSON, _ := json.Marshal(inv.Arguments)
		toolCalls = append(toolCalls, core.ToolCall{
			ID:        inv.ID,
			Name:      inv.Name,
			Arguments: string(argsJSON),
		})
	}
	assistantMsg := core.Message{
		Role:      "assistant",
		Content:   resp.Content,
		Timestamp: time.Now().Unix(),
		ToolCalls: toolCalls,
	}
	*history = append(*history, assistantMsg)
	r.persistStepToStore(assistantMsg)

	for i, tr := range results {
		if tr.ToolName == "answer" {
			continue
		}
		toolCallID := tr.ToolCallID
		if toolCallID == "" && i < len(resp.ToolCalls) {
			toolCallID = resp.ToolCalls[i].ID
		}
		toolMsg := core.Message{
			Role:       "tool",
			Content:    tr.ToolResultSummary(),
			Timestamp:  time.Now().Unix(),
			ToolCallID: toolCallID,
		}
		*history = append(*history, toolMsg)
		r.persistStepToStore(toolMsg)
	}
}

func (r *Reactor) persistStepToStore(msg core.Message) {
	ss := r.llmCaller.SessionStore()
	cw := r.llmCaller.ContextWindow()
	if ss == nil || cw == nil {
		r.llmCaller.AddContextMessage(msg.Role, msg.Content)
		return
	}
	agentName := cw.Role
	r.llmCaller.AddContextMessage(msg.Role, msg.Content)
	if err := ss.Append(context.TODO(), cw.SessionID, agentName, msg); err != nil {
		r.getLogger().Warn("failed to persist step to session store", "session_id", cw.SessionID, "role", msg.Role, "error", err)
	}
}

// ── makeEmitter ─────────────────────────────────────────────────────────────

func (r *Reactor) makeEmitter(sessionID string) func(core.ReactEventType, any) {
	if r.eventBus == nil {
		return func(core.ReactEventType, any) {}
	}
	return func(evtType core.ReactEventType, data any) {
		r.eventBus.Emit(core.ReactEvent{
			SessionID: sessionID,
			Type:      evtType,
			Data:      data,
		})
	}
}

// ── buildResult ─────────────────────────────────────────────────────────────

func (r *Reactor) buildResult(answer, reason string, usage core.TokenUsage, elapsed time.Duration, iterations int) *RunResult {
	return &RunResult{
		Answer:            answer,
		TotalIterations:   iterations,
		TerminationReason: reason,
		TokenUsage:        usage,
		TotalDuration:     elapsed,
	}
}

// ── resolveSessionID ────────────────────────────────────────────────────────

func (r *Reactor) resolveSessionID(history []core.Message) string {
	if cw := r.llmCaller.ContextWindow(); cw != nil && cw.SessionID != "" {
		return cw.SessionID
	}
	return "default"
}

// ── SetModelConfig ──────────────────────────────────────────────────────────

func (r *Reactor) SetModelConfig(model core.ModelConfig) {
	r.config.Model = model.Name
	r.config.Temperature = model.Temperature
	r.config.TopP = model.TopP
	r.config.TopK = int(model.TopK)
	r.config.PresencePenalty = model.RepetitionPenalty
	r.config.FrequencyPenalty = model.FrequencyPenalty
	r.config.MaxTokens = int(model.MaxTokens)
	r.config.MaxIterations = model.MaxTurns
	r.config.IsLocal = model.IsLocal
	if model.APIKey != "" {
		r.config.APIKey = model.APIKey
	}
	if model.BaseURL != "" {
		r.config.BaseURL = model.BaseURL
	}
	r.rebuildLLMCaller(model)
}

func (r *Reactor) rebuildLLMCaller(model core.ModelConfig) {
	client := gochat.Client().Config(
		gochat.WithAPIKey(model.APIKey),
		gochat.WithBaseURL(model.BaseURL),
		gochat.WithTimeout(4*time.Minute),
	)
	llmCfg := LLMCallerConfig{
		ModelName:        model.Name,
		Temperature:      model.Temperature,
		TopP:             model.TopP,
		TopK:             int(model.TopK),
		PresencePenalty:  model.RepetitionPenalty,
		FrequencyPenalty: model.FrequencyPenalty,
		MaxTokens:        int(model.MaxTokens),
	}
	r.llmCaller.SetConfig(llmCfg)
	r.llmCaller.SetClient(client)
	r.cacheMu.Lock()
	r.cachedLLMTools = nil
	r.cacheMu.Unlock()
}

// ── getLLMTools ─────────────────────────────────────────────────────────────

func (r *Reactor) getLLMTools() []gochatcore.Tool {
	r.cacheMu.RLock()
	if r.cachedLLMTools != nil {
		cached := r.cachedLLMTools
		r.cacheMu.RUnlock()
		return cached
	}
	r.cacheMu.RUnlock()

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	if r.cachedLLMTools != nil {
		return r.cachedLLMTools
	}

	var tools []gochatcore.Tool
	for _, ft := range r.toolRegistry.All() {
		t := ft.Info()
		tools = append(tools, gochatcore.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  mustMarshalJSON(convertParametersToSchema(t.Parameters)),
		})
	}
	r.cachedLLMTools = tools
	return tools
}

// ── CloneReactor ────────────────────────────────────────────────────────────

func (r *Reactor) CloneReactor(configOverride ReactorConfig, opts ...ReactorOption) (*Reactor, error) {
	merged := r.config.Merge(configOverride)
	if configOverride.SystemPrompt == "" {
		merged.SystemPrompt = ""
	}
	child := NewReactor(merged, opts...)

	child.memory = r.memory
	child.sessionMemory = r.sessionMemory
	child.eventBus = r.eventBus
	child.prompt = r.prompt
	child.toolRegistry = r.toolRegistry
	if r.toolExecutor != nil {
		child.toolExecutor = r.toolExecutor
	}
	child.sessionDir = r.sessionDir
	child.projectDir = r.projectDir

	for _, t := range r.toolRegistry.All() {
		_ = child.RegisterTool(t)
	}

	return child, nil
}

func (r *Reactor) RegisterTool(tool core.FuncTool) error {
	if err := r.toolRegistry.Register(tool); err != nil {
		return err
	}
	r.cacheMu.Lock()
	r.cachedLLMTools = nil
	r.cacheMu.Unlock()
	return nil
}

// ── 工具注册 ─────────────────────────────────────────────────────────────────

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

// ── reactorSetup ────────────────────────────────────────────────────────────

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
	projectDir     string
	sessionDir     string
	loopHooks      []LoopHook
	toolHooks      []ToolHook
	stuckAnalyzer  StuckDetector
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
		Logger:           r.getLogger(),
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
		core.WithProjectDirExecutor(setup.projectDir),
		core.WithSessionDirExecutor(setup.sessionDir),
	)
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

// ── NewReactor ─────────────────────────────────────────────────────────────

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

	if config.AsyncToolTimeout > 0 {
		r.asyncToolTimeout = config.AsyncToolTimeout
	} else {
		r.asyncToolTimeout = 5 * time.Minute
	}
	if config.SyncToolTimeout > 0 {
		r.syncToolTimeout = config.SyncToolTimeout
	} else {
		r.syncToolTimeout = 5 * time.Minute
	}

	r.projectDir = setup.projectDir
	r.sessionDir = setup.sessionDir

	if setup.kvStore == nil {
		if kv, err := core.NewFileSystemKVStore(""); err == nil {
			r.kvStore = kv
		} else {
			r.getLogger().Warn("failed to init KVStore", "error", err)
		}
	} else {
		r.kvStore = setup.kvStore
	}
	if setup.fileStore == nil {
		if fs, err := core.NewFileSystemFileStore(""); err == nil {
			r.fileStore = fs
		} else {
			r.getLogger().Warn("failed to init FileStore", "error", err)
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
	for _, name := range setup.excludeTools {
		if err := r.toolRegistry.Remove(name); err != nil {
			r.getLogger().Warn("failed to exclude tool", "name", name, "error", err)
		}
	}
	if len(setup.excludeTools) > 0 {
		r.cacheMu.Lock()
		r.cachedLLMTools = nil
		r.cacheMu.Unlock()
	}

	// Hook 初始化
	r.loopHooks = append(r.loopHooks, setup.loopHooks...)
	r.toolHooks = append(r.toolHooks, setup.toolHooks...)

	// MemoryThoughtHook (read-half of memory closed loop)
	memForHook := setup.sessionMemory
	if memForHook == nil {
		memForHook = setup.memory
	}
	if memForHook != nil {
		r.loopHooks = append(r.loopHooks, NewMemoryThoughtHook(memForHook))
	}

	sortByPriority(r.loopHooks)
	sortByPriority(r.toolHooks)

	if setup.stuckAnalyzer != nil {
		r.stuckAnalyzer = setup.stuckAnalyzer
		r.stuckNudgeCounts = make(map[StuckPattern]int)
	}

	return r
}

// BuildExecutionGuidelines returns empty string (safety guidance now merged into Behavioral Rules).
func BuildExecutionGuidelines() string { return "" }

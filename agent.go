package goreact

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/internal/reactor/hooks/action"
	"github.com/DotNetAge/goreact/internal/reactor/hooks/observation"
	"github.com/DotNetAge/goreact/internal/reactor/hooks/thought"
	"github.com/DotNetAge/goreact/reactor"
	"github.com/google/uuid"
)

// defaultAskTimeout is the timeout applied to Ask/AskStream convenience methods
// when no explicit context.Context is provided by the caller.
const defaultAskTimeout = 5 * time.Minute

// ---------------------------------------------------------------------------
// Default presets
// ---------------------------------------------------------------------------

// DefaultModel returns a ModelConfig pre-configured for a fast, cost-effective model.
// The default uses qwen3.5-flash which provides excellent performance-to-cost ratio.
// Override individual fields as needed (e.g., change BaseURL for a compatible API).
func DefaultModel() *core.ModelConfig {
	return &core.ModelConfig{
		Name:        "qwen3.5-flash",
		Description: "Quick and cost-effective model for general-purpose tasks",
		MaxTokens:   8192,
		Enabled:     true,
	}
}

// filterSkills returns only skills whose names are in the required list.
func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var result []string
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func filterSkills(allSkills []*core.Skill, required []string) []*core.Skill {
	need := make(map[string]bool, len(required))
	for _, name := range required {
		need[name] = true
	}
	var result []*core.Skill
	for _, s := range allSkills {
		if need[s.Name] {
			result = append(result, s)
		}
	}
	return result
}

// DefaultConfig returns an AgentConfig with sensible defaults for a general-purpose agent.
func DefaultConfig() *core.AgentConfig {
	return &core.AgentConfig{
		Name:        "mindx",
		Role:        "assistant",
		Description: "A helpful AI assistant for personal use. It can answer questions, summarize conversations, and perform tasks.",
	}
}

// ---------------------------------------------------------------------------
// Agent — top-level facade
// ---------------------------------------------------------------------------

// Agent is the top-level facade for interacting with the ReAct agent system.
// Users should interact exclusively through this type; knowledge of the internal
// Reactor engine is NOT required.
//
// Quick start:
//
//	agent := goreact.DefaultAgent("your-api-key")
//	answer, err := agent.Ask("Hello!")
type Agent struct {
	config       *core.AgentConfig
	model        *core.ModelConfig
	memory       core.Memory
	reactor      *reactor.Reactor
	eventBus     reactor.EventBus
	lastResult   *Result
	sessionStore core.SessionStore

	interruptMu sync.Mutex
	cancelFunc  context.CancelFunc
	isRunning   bool
}

// ---------------------------------------------------------------------------
// Result — what developers care about after a call
// ---------------------------------------------------------------------------

// Result holds the outcome of an Ask or AskStream call.
// Developers can query token consumption, iterations, tool usage, etc.
type Result struct {
	Answer    string `json:"answer"`
	Tokens    int    `json:"tokens"`
	Duration  string `json:"duration,omitempty"` // human-readable, e.g. "1.23s"
	Steps     int    `json:"steps"`              // total T-A-O iterations
	ToolsUsed int    `json:"tools_used"`         // number of tool invocations
}

// ---------------------------------------------------------------------------
// AgentOption — functional options for NewAgent
// ---------------------------------------------------------------------------
//
// Architecture: WithConfig and WithModel are the two primary options that define
// an Agent's identity and capabilities. All other options are supplementary.
// SystemPrompt belongs to AgentConfig, NOT as a standalone option.
//
// Only options with real extension value for developers are exposed here.
// Internal defense mechanisms use sensible defaults and are NOT exposed to avoid
// unnecessary type leakage. Advanced users can access the internal Reactor
// via Agent.Reactor() for fine-grained control.
// ---------------------------------------------------------------------------

// agentSetup holds all optional configuration collected from AgentOptions.
type agentSetup struct {
	config *core.AgentConfig
	model  *core.ModelConfig

	memory          core.Memory
	sessionMemory   core.Memory
	sessionID string

	// Tools & Skills
	extraTools     []core.FuncTool
	excludeTools   []string
	skipAllBundled bool
	skipToolNames  map[string]bool
	skillDirs      []string

	// Event streaming & security
	eventBus reactor.EventBus

	sessionStore core.SessionStore

	// Behavior rules
	ruleRegistry core.RuleRegistry

	// Unified logging interface (injected from external)
	logger core.Logger

	// Unified Prompt (if nil, built from config defaults)
	prompt *reactor.Prompt

	// Directory context (Design-time safety: guaranteed to be set)
	projectDir string // Layer 2: Working directory at Agent creation time
	sessionDir string // Layer 3: Session directory (from SessionStore or explicit)

	skillNames []string // Registered skill names for all agent

	// Hook 注入
	thoughtHooks     []reactor.ThoughtHook
	toolHooks        []reactor.ToolHook
	// Permission rule store (injected externally, nil = skip rule-based checking)
	permissionRuleStore core.PermissionRuleStore

	observationHooks []reactor.ObservationHook
}

// AgentOption configures an Agent during creation via NewAgent.
type AgentOption func(*agentSetup)

func WithSkills(skills ...string) AgentOption {
	return func(s *agentSetup) {
		s.skillNames = skills
	}
}

// WithConfig sets the AgentConfig that defines the agent's identity:
// name, domain, description, and system prompt.
// If not set, DefaultConfig() is used.
//
//	config := &core.AgentConfig{
//	    Name:        "code-reviewer",
//	    Domain:      "software-engineering",
//	    Description: "A code review assistant",
//	    SystemPrompt: "You are a senior code reviewer...",
//	}
func WithConfig(config *core.AgentConfig) AgentOption {
	return func(s *agentSetup) {
		s.config = config
	}
}

// WithModel sets the ModelConfig that defines the LLM backend:
// model name, API key, base URL, etc.
// If not set, DefaultModel() is used (API key must be set separately or via this option).
//
//	model := goreact.DefaultModel()
//	model.APIKey = "your-api-key"
//	model.BaseURL = "https://api.example.com/v1"
func WithModel(model *core.ModelConfig) AgentOption {
	return func(s *agentSetup) {
		s.model = model
	}
}

// WithMemory sets a Memory implementation for knowledge retrieval and hallucination suppression.
// If not set, the agent operates without memory augmentation.
func WithMemory(mem core.Memory) AgentOption {
	return func(s *agentSetup) {
		s.memory = mem
	}
}

// WithSessionMemory sets a separate short-term/session-scoped Memory instance.
// Unlike WithMemory (which holds long-term/project knowledge), this memory is
// used by the memory closed loop:
//   - MemorySlideHandler stores slid-out context messages here (write-half)
//   - MemoryThoughtHook retrieves from here before each Think phase (read-half)
//
// When set, the closed loop uses this instead of the primary Memory.
// Typically backed by a session-scoped RAG index (SessionRAG).
func WithSessionMemory(mem core.Memory) AgentOption {
	return func(s *agentSetup) {
		s.sessionMemory = mem
	}
}

// WithSession starts a conversation session immediately upon creation.
// sessionID identifies the session.
func WithSession(sessionID string) AgentOption {
	return func(s *agentSetup) {
		s.sessionID = sessionID
	}
}

// WithExtraTools adds custom tools to the agent.
// Each tool must implement core.FuncTool (Info() + Execute()).
func WithExtraTools(tools ...core.FuncTool) AgentOption {
	return func(s *agentSetup) {
		s.extraTools = append(s.extraTools, tools...)
	}
}

// WithExcludeTools removes tools from the ToolRegistry by name after all
// tools (bundled, extra, skill-registered) have been registered. This is
// useful for selectively disabling specific tools from skills or the bundle
// without disabling entire skill sets.
func WithExcludeTools(names ...string) AgentOption {
	return func(s *agentSetup) {
		s.excludeTools = append(s.excludeTools, names...)
	}
}

// WithoutBundledTools skips registration of all built-in tools (orchestration tools are still registered).
func WithoutBundledTools() AgentOption {
	return func(s *agentSetup) {
		s.skipAllBundled = true
	}
}

// WithoutTool skips registration of a specific built-in tool by name.
func WithoutTool(name string) AgentOption {
	return func(s *agentSetup) {
		if s.skipToolNames == nil {
			s.skipToolNames = make(map[string]bool)
		}
		s.skipToolNames[name] = true
	}
}

// WithSkillDir loads additional skills from the given directory.
// Each subdirectory should contain a SKILL.md file defining the skill.
// May be called multiple times to load from multiple directories.
func WithSkillDir(dir string) AgentOption {
	return func(s *agentSetup) {
		s.skillDirs = append(s.skillDirs, dir)
	}
}

// WithEventBus sets the event bus for streaming agent-level events (thinking, actions, etc.).
// If not set, an in-process bus is created automatically.
func WithEventBus(bus reactor.EventBus) AgentOption {
	return func(s *agentSetup) {
		s.eventBus = bus
	}
}

// WithSecurityPolicy sets a custom security policy for tool execution.
// The policy is a function that receives (toolName, securityLevel) and returns
// true to allow or false to block execution.
// WithSessionStore sets a SessionStore for conversation persistence.
// If not set, NewAgent falls back to MemorySessionStore (in-memory, no persistence).
func WithSessionStore(store core.SessionStore) AgentOption {
	return func(s *agentSetup) {
		s.sessionStore = store
	}
}

// WithProjectDir sets the project working directory for this Agent.
// This is critical for tool execution (edit/read/write tools need to know where to operate).
//
// Design-time safety guarantee:
//   - If NOT provided, defaults to os.Getwd() at Agent creation time
//   - The value is automatically injected into ToolContext for all tool calls
//   - LLM will always have access to this directory in its system prompt
//   - Prevents runtime failures caused by missing directory context
//
// Usage:
//
//	agent, err := goreact.NewAgent(
//	    goreact.WithConfig(cfg),
//	    goreact.WithProjectDir("/Users/you/project"),  // Explicit
//	)
//	// OR rely on default (os.Getwd()):
//	agent, err := goreact.NewAgent(goreact.WithConfig(cfg))
func WithProjectDir(dir string) AgentOption {
	return func(s *agentSetup) {
		s.projectDir = dir
	}
}

// WithSessionDir sets the session directory for this Agent.
// This provides a session-scoped working directory (temp files, drafts, etc.).
//
// When to use:
//   - When you have an existing Session and want to bind the Agent to it
//   - When SessionStore is available but you want to override the default resolution
//
// Design-time safety:
//   - If NOT provided and SessionStore exists, may be auto-resolved on first Call
//   - If provided, takes precedence over auto-resolution
//   - The value is injected into ToolContext.SessionDir for all tool calls
func WithSessionDir(dir string) AgentOption {
	return func(s *agentSetup) {
		s.sessionDir = dir
	}
}

// WithPrompt sets a custom Prompt struct for system prompt generation.
// If not set, NewAgent builds a default Prompt from the AgentConfig.
// This replaces the older SystemPrompt approach with the structured Prompt sections.
func WithPrompt(p *reactor.Prompt) AgentOption {
	return func(s *agentSetup) {
		s.prompt = p
	}
}

// WithRuleRegistry sets a custom RuleRegistry implementation for behavioral rules.
// Rules are injected into the System Prompt's ## Behavioral Rules section.
// There is no built-in default — users must provide their own implementation.
//
// Example:
//
//	reg := myRuleRegistry{}
//	reg.Register(core.Rule{ID: "no-delete", Intro: "Never delete production data files."})
//	agent := NewAgent(WithRuleRegistry(reg))
func WithRuleRegistry(reg core.RuleRegistry) AgentOption {
	return func(s *agentSetup) {
		s.ruleRegistry = reg
	}
}

// WithLogger sets a unified logging interface for the Agent and all its internal components
// (Reactor, ToolExecutor, Tools). This enables external log management (Zap, Logrus, etc.).
//
// Example (MindX integration):
//
//	import "github.com/DotNetAge/mindx/pkg/logging"
//
//	zapLogger := logging.DefaultZapLogger(&logging.ZapConfig{
//	    Filename: "logs/app.log",
//	    Console: true,
//	})
//	agent := goreact.NewAgent(
//	    goreact.WithModel(model),
//	    goreact.WithLogger(zapLogger),  // ← All logs go through Zap
//	)
func WithThoughtHooks(hooks ...reactor.ThoughtHook) AgentOption {
	return func(s *agentSetup) {
		s.thoughtHooks = append(s.thoughtHooks, hooks...)
	}
}

func WithToolHooks(hooks ...reactor.ToolHook) AgentOption {
	return func(s *agentSetup) {
		s.toolHooks = append(s.toolHooks, hooks...)
	}
}

func WithObservationHooks(hooks ...reactor.ObservationHook) AgentOption {
	return func(s *agentSetup) {
		s.observationHooks = append(s.observationHooks, hooks...)
	}
}

func WithPermissionRuleStore(store core.PermissionRuleStore) AgentOption {
	return func(s *agentSetup) {
		s.permissionRuleStore = store
	}
}

func WithLogger(logger core.Logger) AgentOption {
	return func(s *agentSetup) {
		s.logger = logger
	}
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// buildReactorConfig creates a ReactorConfig from ModelConfig and AgentConfig.
// This centralizes the field mapping to avoid duplication across NewAgent, Clone, and Switch.
func buildReactorConfig(model *core.ModelConfig, systemPrompt string) reactor.ReactorConfig {
	return reactor.ReactorConfig{
		APIKey:           model.APIKey,
		BaseURL:          model.BaseURL,
		AuthToken:        model.AuthToken,
		Model:            model.Name,
		SystemPrompt:     systemPrompt,
		IsLocal:          model.IsLocal,
		Temperature:      model.Temperature,
		TopP:             model.TopP,
		TopK:             int(model.TopK),
		PresencePenalty:  model.RepetitionPenalty,
		FrequencyPenalty: model.RepetitionPenalty,
		MaxTokens:        int(model.MaxTokens),
		MaxIterations:    model.MaxTurns,
	}
}

// DefaultAgent creates a ready-to-use Agent with sensible defaults.
// It only requires an API key to start working. The agent uses qwen3.5-flash
// by default with a standard T-A-O reactor and a session context window of 8192 tokens.
//
// Usage:
//
//	agent := goreact.DefaultAgent("your-api-key")
//	answer, err := agent.Ask("Hello, how are you?")
func DefaultAgent(apiKey string) (*Agent, error) {
	model := DefaultModel()
	model.APIKey = apiKey

	// NOTE: MaxTokens below 40K is insufficient for most general-purpose tasks.
	return NewAgent(
		WithModel(model),
		WithSession(uuid.NewString()),
	)
}

// NewAgent creates an Agent configured entirely through options.
// WithConfig and WithModel define the agent's core identity and LLM backend;
// all other options are supplementary.
//
// Minimal usage:
//
//	agent := goreact.NewAgent(
//	    goreact.WithConfig(&core.AgentConfig{
//	        Name:        "my-agent",
//	        Domain:      "general",
//	        SystemPrompt: "You are a helpful assistant.",
//	    }),
//	    goreact.WithModel(model),
//	)
//
// One-liner with defaults:
//
//	model := goreact.DefaultModel()
//	model.APIKey = "your-api-key"
//	agent := goreact.NewAgent(goreact.WithModel(model))
//
// Full-featured:
//
//	agent := goreact.NewAgent(
//	    goreact.WithConfig(config),
//	    goreact.WithModel(model),
//	    goreact.WithMemory(mem),
//	    goreact.WithExtraTools(myTool),
//	    goreact.WithSkillDir("/path/to/skills"),
//	    goreact.WithSession("s1"),
//	    goreact.WithSecurityPolicy(policy),
//	)
func NewAgent(opts ...AgentOption) (*Agent, error) {
	setup := &agentSetup{}
	for _, opt := range opts {
		opt(setup)
	}

	// Apply defaults if not provided
	if setup.config == nil {
		setup.config = DefaultConfig()
	}
	if setup.model == nil {
		setup.model = DefaultModel()
	}

	// Design-time safety: Ensure ProjectDir is always set
	// This prevents runtime failures where LLM lacks directory context
	if setup.projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "." // Fallback to current directory
		}
		setup.projectDir = cwd
	}

	config := setup.config
	model := setup.model

	// Validate required fields
	if !model.Enabled {
		return nil, fmt.Errorf("goreact: model %q is not enabled — set Enabled=true or configure API key first", model.Name)
	}
	if model.APIKey == "" {
		return nil, fmt.Errorf("goreact: ModelConfig.APIKey is required, got empty. Use goreact.WithModel(model) where model.APIKey is set")
	}

	// Build reactor options — only forward options with real extension value
	var reactorOpts []reactor.ReactorOption
	if setup.memory != nil {
		reactorOpts = append(reactorOpts, reactor.WithMemory(setup.memory))
	}
	if setup.sessionMemory != nil {
		reactorOpts = append(reactorOpts, reactor.WithSessionMemory(setup.sessionMemory))
	}
	if setup.eventBus != nil {
		reactorOpts = append(reactorOpts, reactor.WithEventBus(setup.eventBus))
	}
	if len(setup.extraTools) > 0 {
		reactorOpts = append(reactorOpts, reactor.WithExtraTools(setup.extraTools...))
	}
	if len(setup.excludeTools) > 0 {
		reactorOpts = append(reactorOpts, reactor.WithExcludeTools(setup.excludeTools...))
	}
	if setup.skipAllBundled {
		reactorOpts = append(reactorOpts, reactor.WithoutBundledTools())
	}
	for name := range setup.skipToolNames {
		reactorOpts = append(reactorOpts, reactor.WithoutTool(name))
	}
	for _, dir := range setup.skillDirs {
		reactorOpts = append(reactorOpts, reactor.WithSkillDir(dir))
	}

	if setup.sessionStore == nil {
		setup.sessionStore = core.NewMemorySessionStore()
	}
	reactorOpts = append(reactorOpts, reactor.WithSessionStore(setup.sessionStore))
	if setup.ruleRegistry != nil {
		reactorOpts = append(reactorOpts, reactor.WithRuleRegistry(setup.ruleRegistry))
	}

	// Design-time safety: Always inject ProjectDir to Reactor (guaranteed non-empty after defaults)
	reactorOpts = append(reactorOpts, reactor.WithProjectDir(setup.projectDir))

	// Inject SessionDir if explicitly provided (Layer 3 safety)
	if setup.sessionDir != "" {
		reactorOpts = append(reactorOpts, reactor.WithSessionDir(setup.sessionDir))
	}

	// Hook 注入
	if len(setup.thoughtHooks) > 0 {
		reactorOpts = append(reactorOpts, reactor.WithThoughtHooks(setup.thoughtHooks...))
	}
	if len(setup.toolHooks) > 0 {
		reactorOpts = append(reactorOpts, reactor.WithToolHooks(setup.toolHooks...))
	}
	if len(setup.observationHooks) > 0 {
		reactorOpts = append(reactorOpts, reactor.WithObservationHooks(setup.observationHooks...))
	}

	// Build ReactorConfig from ModelConfig — align all generation parameters
	reactorConfig := buildReactorConfig(model, config.Introduction)

	if setup.logger != nil {
		reactorConfig.Logger = setup.logger
	}

	// Build default Prompt if none provided
	if setup.prompt == nil {
		p := reactor.NewDefaultPrompt(config.Name, config.Role, config.Description, config.Introduction)
		p.ExecutionGuidelines = reactor.BuildExecutionGuidelines()
		p.ToolUsage = reactor.BuildToolUsageGuidelines()
		// if config.EnableOrchestration {
		// p.AgentCoordination = reactor.BuildAgentCoordinationGuidance()
		// }
		p.ToneAndStyle = reactor.BuildToneAndStyle()
		p.SystemReminders = reactor.BuildSystemReminders()
		p.OutputEfficiency = reactor.BuildOutputEfficiency()
		reactorOpts = append(reactorOpts, reactor.WithPrompt(p))
	} else {
		reactorOpts = append(reactorOpts, reactor.WithPrompt(setup.prompt))
	}
	// Apply orchestration mode from agent config
	// reactorOpts = append(reactorOpts, reactor.WithEnableOrchestration(config.EnableOrchestration))

	r := reactor.NewReactor(reactorConfig, reactorOpts...)

	// Register default lifecycle hooks (user hooks already registered as ReactorOptions)
	r.RegisterThoughtHooks(thought.Defaults(r.Logger())...)
	r.RegisterToolHooks(action.Defaults(r.AskPermission(), setup.permissionRuleStore, r.SkillRegistry(), r.BudgetEnforcer(), r.Logger())...)
	r.RegisterObservationHooks(observation.Defaults(r.Logger())...)

	// Populate skills catalog and rules on the Prompt
	if p := r.Prompt(); p != nil {
		// Only inject skills that the agent explicitly declared in its config or registered via WithSkills.
		// Progressive disclosure: we do NOT dump all loaded skills into the prompt.
		// The LLM discovers additional skills on-demand via the Skill tool.
		mergedSkillNames := mergeUniqueStrings(config.Skills, setup.skillNames)
		if len(mergedSkillNames) > 0 {
			skills := r.SkillRegistry().ListSkills()
			filtered := filterSkills(skills, mergedSkillNames)
			if len(filtered) > 0 {
				if catalog := reactor.BuildSkillsCatalog(filtered); catalog != "" {
					p.SkillsCatalog = catalog
				}
			}
		}

		// Merge custom rules from RuleRegistry into Prompt.Rules (if set)
		if reg := r.RuleRegistry(); reg != nil {
			if rules := reg.FormatPromptSection(); rules != "" {
				if p.Rules != "" {
					p.Rules += "\n" + rules
				} else {
					p.Rules = rules
				}
			}
		}
	}

	// Set SpawnFunc so delegate tool can create sub-agents
	r.SpawnFunc = func(ctx context.Context, agentName, task string) (string, error) {
		subConfig := *config
		subConfig.Name = agentName
		sub, err := NewAgent(
			WithConfig(&subConfig),
			WithModel(model),
			WithEventBus(r.EventBus()),
			WithMemory(setup.memory),
		)
		if err != nil {
			return "", fmt.Errorf("create sub-agent %q: %w", agentName, err)
		}
		result, err := sub.Ask(fmt.Sprintf("sub-%s", agentName), task)
		if err != nil {
			return "", fmt.Errorf("sub-agent %q execution: %w", agentName, err)
		}
		return result.Answer, nil
	}

	a := &Agent{
		config:       config,
		model:        model,
		memory:       setup.memory,
		reactor:      r,
		eventBus:     r.EventBus(),
		sessionStore: setup.sessionStore,
	}

	if setup.sessionID != "" {
		maxTokens := int64(131072)
		if a.model != nil {
			maxTokens = int64(a.model.MaxTokens)
		}
		a.reactor.SetContextWindow(core.NewContextWindow(setup.sessionID, maxTokens))
	}

	return a, nil
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

// Config returns the agent's configuration.
func (a *Agent) Config() *core.AgentConfig {
	return a.config
}

// Model returns the agent's model configuration.
func (a *Agent) Model() *core.ModelConfig {
	return a.model
}

// SetModel updates the agent's model configuration at runtime.
// Changes propagate to the Reactor and ultimately the gochat LLM client,
// taking effect on the next Ask/AskStream call. Conversation history and
// context window are preserved.
//
// Safe to call between Ask/AskStream calls. Do NOT call concurrently with
// a running Ask/AskStream — Cancel() first if needed.
//
//	model := goreact.DefaultModel()
//	model.Name = "qwen3.5-32b"
//	model.Temperature = 0.3
//	agent.SetModel(model)
func (a *Agent) SetModel(model *core.ModelConfig) {
	a.interruptMu.Lock()
	defer a.interruptMu.Unlock()

	if model == nil {
		return
	}
	// Update the local model config (copy to avoid external mutation)
	cp := *model
	a.model = &cp

	a.reactor.SetModelConfig(cp)
}

func (a *Agent) Name() string {
	return a.config.Name
}

func (a *Agent) Role() string {
	return a.config.Role
}

func (a *Agent) Description() string {
	return a.config.Description
}

// Memory returns the agent's memory instance, or nil if not configured.
func (a *Agent) Memory() core.Memory {
	return a.memory
}

// ContextWindow returns the agent's context window (delegated to Reactor).
func (a *Agent) ContextWindow() *core.ContextWindow {
	return a.reactor.ContextWindow()
}

// Reactor returns the internal Reactor for advanced use cases.
// Most users should NOT need this; it is exposed for scenarios that require
// direct Reactor access (e.g., registering tools at runtime, accessing internal
// registries, or fine-tuning defense mechanisms).
func (a *Agent) Reactor() *reactor.Reactor {
	return a.reactor
}

// SessionStore returns the agent's session store, or nil if not configured.
func (a *Agent) SessionStore() core.SessionStore {
	return a.sessionStore
}

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

// NewSession starts a new conversation session, replacing any existing one.
// The session is automatically bound to the agent's current config name as its role,
// so that sessions are isolated per role and never shared across agents.
// maxTokens is read from the agent's model config internally.
func (a *Agent) NewSession(sessionID string) {
	maxTokens := int64(131072)
	if a.model != nil {
		maxTokens = int64(a.model.MaxTokens)
	}
	cw := core.NewContextWindowWithRole(sessionID, a.config.Name, maxTokens)
	a.reactor.SetContextWindow(cw)

	// Register the role binding in the session store for later lookup by Switch()
	if ss, ok := a.sessionStore.(*core.MemorySessionStore); ok {
		ss.RegisterRole(sessionID, a.config.Name)
	}
}

// GetSessionByRole returns the most recent session ID and context window
// for the given role. This is used internally by Agent.Switch() to resume
// the latest session for a role instead of creating a new one each time.
func (a *Agent) GetSessionByRole(role string) (*core.SessionInfo, error) {
	return a.sessionStore.GetByRole(context.Background(), role)
}

// ListSessions returns all known sessions from the store, sorted by most recent.
func (a *Agent) ListSessions() ([]core.SessionInfo, error) {
	return a.sessionStore.ListSessions(context.Background())
}

// SessionID returns the current session ID, or empty string if no session.
func (a *Agent) SessionID() string {
	if cw := a.reactor.ContextWindow(); cw != nil {
		return cw.SessionID
	}
	return ""
}

// ---------------------------------------------------------------------------
// Conversation — session-aware entry points
// ---------------------------------------------------------------------------

// Ask sends a question to the Agent and returns a Result with the answer,
// token usage, and execution statistics.
//
// The sessionID identifies which conversation history to use. Pass empty string
// to use the currently bound session (set via NewSession or WithSession).
//
// Agent layer responsibilities (this is where session identity matters):
//   - Rebuilds full ConversationHistory from SessionStore using sessionID
//   - Persists user input and assistant response to SessionStore
//   - Manages ContextWindow sliding
//   - Then passes the fully-assembled history to Reactor.Run (pure executor)
//
// Usage:
//
//	agent := goreact.DefaultAgent("your-api-key")
//	// Use current bound session:
//	result, err := agent.Ask("", "What is AI?")
//	// Or target a specific session:
//	result, err := agent.Ask("session-abc", "Continue our discussion about AI")
//
// Ask sends a question to the agent in the given session.
// A default timeout of defaultAskTimeout is applied to prevent indefinite blocking.
// For fine-grained timeout control, use AskWithContext instead.
func (a *Agent) Ask(sessionID string, question string) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultAskTimeout)
	defer cancel()
	return a.AskWithContext(ctx, sessionID, question)
}

// AskWithSession is a convenience alias for Ask("", question) that uses
// the currently bound session. This preserves backward compatibility with
// the pre-refactor signature agent.Ask(question).
func (a *Agent) AskWithSession(question string) (*Result, error) {
	return a.Ask(a.SessionID(), question)
}

// AskWithContext is like Ask but accepts an explicit context.Context for cancellation
// and timeout control. Most users should use Ask; this is for advanced scenarios
// such as request-scoped deadlines or graceful shutdown.
func (a *Agent) AskWithContext(ctx context.Context, sessionID string, question string) (*Result, error) {
	runCtx, cancel := context.WithCancel(ctx)
	askStart := time.Now()

	a.interruptMu.Lock()
	a.cancelFunc = cancel
	a.isRunning = true
	a.interruptMu.Unlock()

	defer func() {
		cancel()
		a.interruptMu.Lock()
		a.cancelFunc = nil
		a.isRunning = false
		a.interruptMu.Unlock()
	}()

	// Resolve session identity
	effectiveSessionID := sessionID
	if effectiveSessionID == "" {
		effectiveSessionID = a.SessionID()
	}

	logger := a.reactor.Logger()
	logger.Info("[agent] ask start",
		"session_id", effectiveSessionID,
		"question_preview", reactor.Truncate(question, 80),
	)

	// Ensure ContextWindow is set with the correct sessionID before execution
	cw := a.reactor.ContextWindow()
	if cw == nil || cw.SessionID != effectiveSessionID {
		cw = core.NewContextWindowWithRole(effectiveSessionID, a.Name(), int64(a.model.MaxTokens))
		a.reactor.SetContextWindow(cw)
	}

	// 1. Build conversation history from SessionStore (does NOT include current question)
	history := a.buildHistory(ctx, effectiveSessionID)
	logger.Info("[agent] history built",
		"session_id", effectiveSessionID,
		"history_msg_count", len(history),
	)

	// 2. Execute via Reactor — user message is added by assembleMessages via UserMessage
	runResult, err := a.reactor.Run(runCtx, question, reactor.ConversationHistory(history))

	elapsedMs := time.Since(askStart).Milliseconds()
	if err != nil {
		logger.Error("[agent] ask failed", err,
			"session_id", effectiveSessionID,
			"elapsed_ms", elapsedMs,
		)
		return nil, err
	}
	logger.Info("[agent] ask done",
		"session_id", effectiveSessionID,
		"elapsed_ms", elapsedMs,
		"total_iterations", runResult.TotalIterations,
		"total_tokens", runResult.TokensUsed,
	)

	// 3. Persist user message and assistant response after execution
	if runResult.Answer != "" {
		a.persistMessage(ctx, effectiveSessionID, "user", question)
		a.persistMessage(ctx, effectiveSessionID, "assistant", runResult.Answer)
		a.checkSlide(ctx, effectiveSessionID)
	}

	result := &Result{
		Answer:    runResult.Answer,
		Tokens:    runResult.TokensUsed,
		Duration:  runResult.TotalDuration.String(),
		Steps:     runResult.TotalIterations,
		ToolsUsed: len(runResult.Steps),
	}
	a.lastResult = result
	return result, nil
}

// AskStream sends a question and returns a channel that streams text fragments
// as they are produced by the reactor. See Ask for session semantics.
//
// AskStream sends a question and streams response fragments via channel.
// A default timeout of defaultAskTimeout is applied to prevent indefinite blocking.
// For fine-grained timeout control, use AskStreamWithContext instead.
func (a *Agent) AskStream(sessionID string, question string) (<-chan string, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultAskTimeout)
	textCh, innerCancel, _ := a.AskStreamWithContext(ctx, sessionID, question)
	return textCh, func() { innerCancel(); cancel() }, nil
}

// AskStreamWithSession is a convenience alias using the currently bound session.
func (a *Agent) AskStreamWithSession(question string) (<-chan string, func(), error) {
	return a.AskStream(a.SessionID(), question)
}

// AskStreamWithContext is like AskStream but accepts an explicit context.Context.
func (a *Agent) AskStreamWithContext(ctx context.Context, sessionID string, question string) (<-chan string, func(), error) {
	ctx, cancel := context.WithCancel(ctx)

	eventCh, eventCancel := a.eventBus.SubscribeFiltered(func(e core.ReactEvent) bool {
		return e.Type == core.ThinkingDelta || e.Type == core.FinalAnswer
	})

	textCh := make(chan string, reactor.StreamChannelBufferSize)
	closeOnce := sync.Once{}
	closeTextCh := func() {
		closeOnce.Do(func() { close(textCh) })
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		defer eventCancel()
		defer closeTextCh()

		result, err := a.AskWithContext(ctx, sessionID, question)
		if err != nil {
			select {
			case textCh <- fmt.Sprintf("[error] %v", err):
			case <-ctx.Done():
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
			if result.Answer != "" {
				select {
				case textCh <- result.Answer:
				case <-ctx.Done():
				}
			}
		}
	}()

	go func() {
		defer closeTextCh()
		for {
			select {
			case event, ok := <-eventCh:
				if !ok {
					return
				}
				switch data := event.Data.(type) {
				case string:
					select {
					case textCh <- data:
					case <-ctx.Done():
						return
					}
				}
			case <-done:
				drainEvents(eventCh)
				return
			case <-ctx.Done():
				drainEvents(eventCh)
				return
			}
		}
	}()

	return textCh, func() { cancel() }, nil
}

func drainEvents(ch <-chan core.ReactEvent) {
	for range ch {
	}
}

// ---------------------------------------------------------------------------
// Session-aware helpers (Agent-layer session management)
// ---------------------------------------------------------------------------

// historyTokenBudgetRatio determines what fraction of MaxTokens is allocated
// to conversation history when rebuilding context for a session.
const historyTokenBudgetRatio = 0.7

// buildHistory rebuilds the complete ConversationHistory for a session from
// the SessionStore. It uses CurrentContext(agentName) which looks up the most
// recent session for this agent and returns messages within token budget.
// This is called BEFORE each Reactor.Run so the executor receives a fully
// assembled context window with real historical messages.
func (a *Agent) buildHistory(ctx context.Context, sessionID string) []core.Message {
	if a.sessionStore == nil || sessionID == "" {
		return nil
	}
	maxTokensForHistory := int64(float64(a.model.MaxTokens) * historyTokenBudgetRatio)
	msgs, err := a.sessionStore.CurrentContext(ctx, a.Name(), maxTokensForHistory)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	return msgs
}

// persistMessage writes a message to both ContextWindow and SessionStore.
// This replaces the old Reactor.persistMessage — session persistence now lives
// at the Agent layer where session identity is known.
func (a *Agent) persistMessage(ctx context.Context, sessionID, role, content string) {
	if a.sessionStore == nil || sessionID == "" {
		return
	}

	cw := a.reactor.ContextWindow()
	if cw == nil || cw.SessionID != sessionID {
		cw = core.NewContextWindow(sessionID, int64(a.model.MaxTokens))
		a.reactor.SetContextWindow(cw)
	}

	msg := core.Message{Role: role, Content: content, Timestamp: time.Now().Unix()}
	cw.AddMessageWithTimestamp(role, content, msg.Timestamp)
	a.sessionStore.Append(ctx, sessionID, a.Name(), msg)
}

// checkSlide triggers context window sliding if the token budget is exceeded.
// Moved from Reactor to Agent layer — session state management belongs here.
func (a *Agent) checkSlide(ctx context.Context, sessionID string) {
	if a.sessionStore == nil || sessionID == "" {
		return
	}

	cw := a.reactor.ContextWindow()
	if cw == nil {
		return
	}

	slideConfig := a.reactor.SlideConfig()
	if !cw.SlideTriggered(slideConfig) {
		return
	}

	estimateFn := func(s string) int { return a.reactor.EstimateTokens(s) }
	slided := cw.Slide(slideConfig, estimateFn)

	if len(slided.Messages) > 0 {
		event := core.SlideEvent{
			SessionID: sessionID,
			Slided:    slided.Messages,
			Remaining: cw.MessageCount(),
			Timestamp: time.Now().Unix(),
		}
		core.EmitSlideEvent(nil, ctx, event)
	}
}

// ---------------------------------------------------------------------------
// Interruption — Cancel
// ---------------------------------------------------------------------------

// Cancel interrupts the currently running Ask/AskStream call.
// The Run will return with partial results and TerminationReason "request cancelled".
// If no Run is in progress, this is a no-op.
func (a *Agent) Cancel() {
	a.interruptMu.Lock()
	defer a.interruptMu.Unlock()
	if a.cancelFunc != nil {
		a.cancelFunc()
	}
}

// IsRunning returns true if an Ask/AskStream call is currently in progress.
func (a *Agent) IsRunning() bool {
	a.interruptMu.Lock()
	defer a.interruptMu.Unlock()
	return a.isRunning
}

// LastResult returns the Result from the most recent Ask/AskStream call, or nil.
// This is the primary way to inspect token usage, step count, and duration
// after a call completes.
func (a *Agent) LastResult() *Result {
	return a.lastResult
}

// Events subscribes to all agent events (thinking, tool calls, errors, etc.)
// and returns a read-only channel and a cancel function.
//
// Call this BEFORE Ask/AskStream to receive events from the next call.
// Each event is a core.ReactEvent with a Type field for routing:
//
//   - ThinkingDelta: text fragment (streaming thought)
//   - ThinkingDone: completed thought
//   - ActionStart / ActionResult: tool execution
//   - FinalAnswer: the complete answer
//   - Error: reactor-level errors
//   - ExecutionSummary: iteration count, tool usage, token stats
//
// Usage:
//
//	ch, cancel := agent.Events()
//	defer cancel()
//	result, _ := agent.Ask("Summarize this article")
//	for event := range ch {
//	    fmt.Printf("[%s] %v\n", event.Type, event.Data)
//	}
//	fmt.Printf("Total tokens: %d\n", result.Tokens)
func (a *Agent) Events() (<-chan core.ReactEvent, func()) {
	return a.eventBus.Subscribe()
}

// EventsFiltered subscribes to events matching the given filter.
// This is useful when you only care about specific event types.
//
// Usage — only receive thinking and tool action events:
//
//	ch, cancel := agent.EventsFiltered(func(e core.ReactEvent) bool {
//	    return e.Type == core.ThinkingDelta || e.Type == core.ActionStart
//	})
//	defer cancel()
func (a *Agent) EventsFiltered(filter func(core.ReactEvent) bool) (<-chan core.ReactEvent, func()) {
	return a.eventBus.SubscribeFiltered(filter)
}

// Clone creates a child Agent that inherits all runtime state from the parent
// except identity (Config) and model backend. The child shares memory, event bus,
// session store, and all registries with the parent, but has its own independent
// T-A-O loop, conversation context, and task tracking.
//
// When childConfig is nil, inherits parent's config.
// When childModel is nil, inherits parent's model.
//
// Use cases:
//   - SubAgent creation: Clone with a different system prompt for a subtask
//   - Role switching: Use Switch() instead when only changing identity within same session
//
// The difference between Clone() and Switch():
//   - Clone(): creates a NEW Agent with INDEPENDENT conversation history (new ContextWindow)
//   - Switch(): reuses existing Agent's conversation history (shared ContextWindow)
func (a *Agent) Clone(childConfig *core.AgentConfig, childModel *core.ModelConfig) *Agent {
	config := childConfig
	if config == nil {
		cp := *a.config
		config = &cp
	}
	model := childModel
	if model == nil {
		mp := *a.model
		model = &mp
	}

	subReactorConfig := buildReactorConfig(model, config.Introduction)

	childReactor := a.reactor.CloneReactor(subReactorConfig)

	childSessionID := fmt.Sprintf("%s-%s", config.Name, uuid.New().String()[:8])
	childMaxTokens := int64(model.MaxTokens)
	if childMaxTokens <= 0 {
		childMaxTokens = 8192
	}
	childReactor.SetContextWindow(core.NewContextWindow(childSessionID, childMaxTokens))

	return &Agent{
		config:       config,
		model:        model,
		memory:       a.memory,
		reactor:      childReactor,
		eventBus:     a.eventBus,
		sessionStore: a.sessionStore,
		lastResult:   nil,
	}
}

// Switch changes the Agent's identity (Config) and/or model backend while preserving
// all runtime state including memory, event bus, and all registries. This is used for
// in-session role switching — e.g., switching from "developer" to "code-reviewer".
//
// Session handling:
//
//	Switch attempts to resume the most recent session for the target role via
//	GetSessionByRole(). If a previous session exists for that role, its context
//	window is restored so the LLM maintains continuity. If no prior session exists,
//	a fresh context window is created and bound to the new role.
//
// When config is nil, only the model is changed.
// When model is nil, only the config is changed.
//
// Unlike Clone(), Switch() does NOT create a new Agent — it mutates the existing one.
func (a *Agent) Switch(config *core.AgentConfig, model *core.ModelConfig) {
	a.interruptMu.Lock()
	defer a.interruptMu.Unlock()

	if config != nil {
		a.config = config
	}
	if model != nil {
		a.model = model
	}

	switchConfig := buildReactorConfig(a.model, a.config.Introduction)
	existingCW := a.resolveSessionForRole(a.config.Name)

	newReactor := a.reactor.CloneReactor(switchConfig)

	if existingCW != nil {
		newReactor.SetContextWindow(existingCW)
	} else {
		sessionID := fmt.Sprintf("%s-%s", a.config.Name, uuid.New().String()[:8])
		newCW := core.NewContextWindowWithRole(sessionID, a.config.Name, int64(a.model.MaxTokens))
		newReactor.SetContextWindow(newCW)
		if ss, ok := a.sessionStore.(*core.MemorySessionStore); ok {
			ss.RegisterRole(sessionID, a.config.Name)
		}
	}

	a.reactor = newReactor

}

// resolveSessionForRole attempts to restore the most recent session for the given role.
// Returns the ContextWindow with messages loaded, or falls back to current/new window.
func (a *Agent) resolveSessionForRole(role string) *core.ContextWindow {
	if sessInfo, err := a.sessionStore.GetByRole(context.Background(), role); err == nil && sessInfo != nil {
		if msgs, err2 := a.sessionStore.Get(context.Background(), sessInfo.SessionID); err2 == nil && len(msgs) > 0 {
			cw := core.NewContextWindowWithRole(sessInfo.SessionID, role, int64(a.model.MaxTokens))
			for _, m := range msgs {
				cw.AddMessageWithTimestamp(m.Role, m.Content, m.Timestamp)
			}
			if ss, ok := a.sessionStore.(*core.MemorySessionStore); ok {
				ss.RegisterRole(sessInfo.SessionID, role)
			}
			return cw
		}
	}

	currentCW := a.reactor.ContextWindow()
	if currentCW != nil {
		currentCW.Role = role
		return currentCW
	}

	return nil
}

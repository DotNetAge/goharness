// Package agents provides the AI Agent runtime core for executing ReAct (Reasoning + Acting) loops.
//
// This package implements the central orchestration layer for AI agents, managing:
//   - LLM interactions with streaming support and tool use
//   - Tool registration, discovery, and execution (sync/async)
//   - Skill catalogs for agent capabilities
//   - Behavioral rules and system prompt construction
//   - Session management with conversation history and compaction
//   - Hook system for extending loop behavior (BeforeLLM, AfterLLM, Tool hooks)
//   - Event emission for real-time monitoring and observability
//
// # Architecture Overview
//
// The Runtime struct is the central orchestrator that replaces the old Reactor architecture.
// It follows a cleaner separation of concerns:
//
//	Runtime (this file)
//	├── Model Configuration (LLM settings)
//	├── Registries (Tools, Skills, Rules, Agents, Providers)
//	├── Executor (Tool execution engine)
//	├── Hooks (LoopHooks + ToolHooks)
//	└── Logger
//
//	Session (separate package)
//	├── Message storage & retrieval
//	├── Conversation window management
//	├── Compaction (sliding window for token limits)
//	└── Persistence
//
// # Thinking Loop Lifecycle
//
// The core execution flow (exec method) implements a ReAct loop:
//
//  1. Build system prompts from registries + session state
//  2. Execute BeforeLLM hooks (can abort/modify)
//  3. Assemble messages (system + history + user question)
//  4. Stream LLM response (content + thinking + tool calls)
//  5. Execute AfterLLM hooks (can abort/modify)
//  6. If no tool calls → return final answer
//  7. If tool calls → execute tools with hook chain
//  8. Append results to session → repeat from step 1
//  9. Terminate: completed / max_iterations / cancelled / error
//
// # Thread Safety
//
// Runtime is designed for single-threaded use within an Ask context.
// For concurrent Ask calls, create separate Runtime instances or use external synchronization.
// Internal state (registries) is typically initialized once and read during execution.
//
// # Quick Start
//
//	rt := NewRuntime(
//	    WithModel(config.ModelConfig{...}),
//	    WithToolRegistry(customRegistry),
//	)
//	result, err := rt.Ask("agent-name", "What files are in this directory?", session).Execute()
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goreact/config"
	"github.com/DotNetAge/goreact/events"
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/hooks/action"
	"github.com/DotNetAge/goreact/hooks/loop"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/memory"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/session"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

// Runtime holds the infrastructure and registries needed to run the ThinkingLoop.
// It is the replacement for the old Reactor: Runtime + Session together replicate
// all Reactor functionality without the circular architectural issues.
//
// # Architecture Diagram
//
//	┌─────────────────────────────────────────────────────────────┐
//	│                        Runtime                               │
//	├─────────────────────────────────────────────────────────────┤
//	│  model: ModelConfig          (LLM API settings)              │
//	│  ─────────────────────────────────────────────────────────  │
//	│  Registries:                                                 │
//	│    ├─ toolReg: ToolRegistry    (Grep, Bash, WebSearch...)    │
//	│    ├─ skillReg: SkillRegistry  (Agent capabilities)          │
//	│    ├─ ruleReg: RuleRegistry    (Behavioral rules)            │
//	│    ├─ agentReg: AgentRegistry  (Agent configurations)        │
//	│    └─ providerReg: ProviderRegistry (LLM providers)         │
//	│  ─────────────────────────────────────────────────────────  │
//	│  Execution:                                                   │
//	│    ├─ toolExec: ToolExecutor   (Tool execution engine)       │
//	│    └─ mem: Memory             (Vector/memory store)          │
//	│  ─────────────────────────────────────────────────────────  │
//	│  Extensions:                                                  │
//	│    ├─ loopHooks: []LoopHook    (BeforeLLM, AfterLLM, Abort)  │
//	│    ├─ toolHooks: []ToolHook   (Before, After tool execution) │
//	│    └─ logger: Logger          (Structured logging)           │
//	│  ─────────────────────────────────────────────────────────  │
//	│  Timeouts:                                                    │
//	│    ├─ asyncTimeout: Duration   (For async tools)             │
//	│    └─ syncTimeout: Duration    (For sync tools)              │
//	└─────────────────────────────────────────────────────────────┘
//
// # Field Descriptions
//
//   - model: LLM configuration including API key, base URL, model name,
//     temperature, max tokens, and sampling parameters (top_p, top_k, etc.)
//   - toolReg: Registry of available tools (file operations, web access, code execution)
//   - skillReg: Registry of agent skills/capabilities exposed in system prompt
//   - ruleReg: Registry of behavioral rules that constrain agent behavior
//   - mem: Memory interface for vector storage and retrieval (RAG)
//   - agentReg: Registry of named agent configurations (role, description, intro)
//   - providerReg: Registry of LLM provider configurations
//   - toolExec: Tool execution engine with hook support and event emission
//   - logger: Structured logger for debug/info/warning/error output
//   - loopHooks: Hooks that run before/after each LLM call in the thinking loop
//   - toolHooks: Hooks that run before/after each tool execution
//   - asyncTimeout: Maximum execution time for async (concurrent) tools
//   - syncTimeout: Maximum execution time for synchronous (sequential) tools
//
// # Thread Safety
//
// Runtime is NOT safe for concurrent use. Each Ask call should use its own Runtime instance,
// or external synchronization must be applied. The typical pattern is:
//
//   - Create one Runtime per application/service
//   - Use it sequentially for Ask calls, OR
//   - Create a Runtime pool for concurrent processing
//
// Internal registries are typically initialized once at construction time and then read-only
// during execution. If dynamic registration is needed, external locking is required.
type Runtime struct {
	model config.ModelConfig

	toolReg  tools.ToolRegistry
	skillReg skill.SkillRegistry
	ruleReg  rule.RuleRegistry
	mem      memory.Memory

	agentReg    *config.AgentRegistry
	providerReg config.ProviderRegistry
	toolExec    tools.ToolExecutor

	logger    logging.Logger
	loopHooks []hooks.LoopHook
	toolHooks []hooks.ToolHook

	asyncTimeout time.Duration
	syncTimeout  time.Duration

	// tokenUsageStore persists LLM token usage records with grouping dimensions and cost.
	// Default: NoopTokenUsageStore (no-op). Inject via WithTokenUsageStore().
	tokenUsageStore session.TokenUsageStore

	// fileModifyTracker provides TrackFunc per sessionID for FileModifyHook.
	// Set via WithFileModifyTracker() to enable automatic file backup before Write/FileEdit.
	fileModifyTracker action.TrackerProvider

	// fileModifyHook holds a reference to the registered FileModifyHook,
	// allowing WithFileModifyTracker to dynamically update its provider after init.
	fileModifyHook *action.FileModifyHook
}

// RunResult holds execution results from a single Ask call.
// It provides comprehensive information about the thinking loop execution,
// including the final answer, resource usage, and termination details.
//
// # Fields
//
//   - Answer: The final response text from the agent. This is the content
//     returned when the loop terminates normally (no more tool calls needed).
//     If the content is empty but reasoning exists, the reasoning is used.
//   - TokenUsage: Detailed token consumption statistics including input tokens,
//     output tokens, total tokens, and timestamp of usage recording.
//   - Duration: Total wall-clock time for the entire Ask execution, from start
//     to termination (including all iterations, tool executions, and LLM calls).
//   - Iterations: Number of thinking loop iterations completed before termination.
//     Each iteration includes one LLM call and potentially tool executions.
//   - TerminationReason: String indicating why the loop terminated. Possible values:
//     "completed" - Agent provided final answer without tool calls
//     "max_iterations" - Reached maximum iteration limit (default 20)
//     "max_tokens" - LLM response hit token limit (finish_reason: length)
//     "content_filtered" - LLM response was filtered by safety systems
//     "cancelled" - Context was cancelled externally
//     "llm_error" - LLM API call failed (network, auth, rate limit)
//     "hook_error" - A hook panicked or returned an error
//     "hook_abort" - A hook intentionally aborted the loop
//
// # Usage Example
//
//	result, err := rt.Ask("coder", "Explain this code", sess).Execute()
//	if err != nil {
//	    log.Fatalf("Ask failed: %v", err)
//	}
//	fmt.Printf("Answer: %s\n", result.Answer)
//	fmt.Printf("Iterations: %d\n", result.Iterations)
//	fmt.Printf("Tokens: %d\n", result.TokenUsage.TotalTokens)
//	fmt.Printf("Duration: %v\n", result.Duration)
//	fmt.Printf("Termination: %s\n", result.TerminationReason)
type RunResult struct {
	Answer            string
	TokenUsage        session.TokenUsage
	Duration          time.Duration
	Iterations        int
	TerminationReason string
}

// NewRuntime creates a new Runtime instance with the given configuration options.
// It initializes all default registries, tools, and sets sensible defaults for timeouts.
//
// # Parameters
//
// opts: Variadic list of RuntimeConfig functions that configure the Runtime.
//
//	    Available options include:
//	- WithModel(config.ModelConfig): Set LLM model configuration (required)
//	- WithToolRegistry(tools.ToolRegistry): Use custom tool registry
//	- WithSkillRegistry(skill.SkillRegistry): Use custom skill registry
//	- WithRuleRegistry(rule.RuleRegistry): Use custom rule registry
//	- WithAgentRegistry(*config.AgentRegistry): Use custom agent registry
//	- WithProviderRegistry(config.ProviderRegistry): Use custom provider registry
//	- WithMemory(memory.Memory): Set memory/RAG backend
//	- WithLogger(logging.Logger): Set custom logger
//	- WithLoopHooks(...hooks.LoopHook): Add loop lifecycle hooks
//	- WithToolHooks(...hooks.ToolHook): Add tool execution hooks
//	- WithAsyncTimeout(time.Duration): Set timeout for async tools (default: 5min)
//	- WithSyncTimeout(time.Duration): Set timeout for sync tools (default: 5min)
//
// # Default Behavior
//
// When created without options, NewRuntime provides:
//   - DefaultToolRegistry with 15+ built-in tools (Grep, Glob, Read, Write, Bash, etc.)
//   - DefaultSkillRegistry (empty, ready for registration)
//   - DefaultLogger (stdout with structured JSON output)
//   - 5 minute timeouts for both sync and async tool execution
//   - No agent registry, rule registry, or memory (nil)
//   - No hooks registered
//
// # Built-in Tools
//
// The following tools are automatically registered:
//
//	File Operations: Grep, Glob, Read, Write, FileEdit, Ls
//	Execution: Bash, RunScript
//	Web Access: WebSearch, WebFetch
//	Interaction: AskUser
//	Task Management: CollectResults, TaskList, TaskGet, TaskUpdate
//	Platform-specific: PowerShell (Windows only)
//
// # Return Value
//
// *Runtime: A fully initialized Runtime ready for use. The Runtime is NOT
//
//	safe for concurrent use. Create separate instances or synchronize externally.
//
// # Example: Minimal Setup
//
//	rt := NewRuntime(
//	    WithModel(config.ModelConfig{
//	        Name:      "gpt-4",
//	        APIKey:    "sk-...",
//	        BaseURL:   "https://api.openai.com/v1",
//	        MaxTokens: 4096,
//	    }),
//	)
//
// # Example: Full Configuration
//
//	customTools := tools.NewDefaultToolRegistry()
//	customTools.Register(tools.NewCustomTool())
//
//	rt := NewRuntime(
//	    WithModel(config.ModelConfig{
//	        Name:               "claude-3-opus",
//	        APIKey:             "sk-ant-...",
//	        BaseURL:            "https://api.anthropic.com",
//	        MaxTokens:          8192,
//	        Temperature:        0.7,
//	        TopP:              0.9,
//	        MaxTurns:          30,
//	    }),
//	    WithToolRegistry(customTools),
//	    WithSkillRegistry(mySkillReg),
//	    WithRuleRegistry(myRuleReg),
//	    WithMemory(vectorStore),
//	    WithLoopHooks(&MyLoggingHook{}),
//	    WithToolHooks(&SecurityHook{}),
//	    WithAsyncTimeout(10 * time.Minute),
//	    WithSyncTimeout(2 * time.Minute),
//	)
func NewRuntime(opts ...RuntimeConfig) *Runtime {
	r := &Runtime{
		toolReg:      tools.NewDefaultToolRegistry(),
		skillReg:     skill.NewDefaultSkillRegistry(),
		logger:       logging.DefaultLogger(),
		asyncTimeout: 5 * time.Minute,
		syncTimeout:  5 * time.Minute,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.tokenUsageStore == nil {
		r.tokenUsageStore = session.NewNoopTokenUsageStore()
	}
	r.toolExec = tools.NewToolExecutor(r.toolReg)
	r.registerDefaultTools()
	r.registerDefaultHooks()
	return r
}

// ── Default tool registration ───────────────────────────────────────────────

// registerDefaultTools registers all built-in tools into the tool registry.
// Called automatically by NewRuntime during initialization.
// Includes file operations (Grep, Glob, Read, Write, FileEdit, Ls),
// execution tools (Bash, RunScript), web tools (WebSearch, WebFetch),
// interaction tools (AskUser), and task management tools.
// On Windows, additionally registers PowerShell tool.
func (rt *Runtime) registerDefaultTools() {
	bundled := []struct {
		name    string
		factory func() tools.FuncTool
	}{
		{"Grep", func() tools.FuncTool { return tools.NewGrepTool() }},
		{"Glob", func() tools.FuncTool { return tools.NewGlobTool() }},
		{"Read", func() tools.FuncTool { return tools.NewReadTool() }},
		{"Write", func() tools.FuncTool { return tools.NewWriteTool() }},
		{"FileEdit", func() tools.FuncTool { return tools.NewFileEditTool() }},
		{"Bash", func() tools.FuncTool { return tools.NewBashTool() }},
		{"RunScript", func() tools.FuncTool { return tools.NewRunScriptTool() }},
		{"WebSearch", func() tools.FuncTool { return tools.NewWebSearchTool() }},
		{"WebFetch", func() tools.FuncTool { return tools.NewWebFetchTool() }},
		{"AskUser", func() tools.FuncTool { return tools.NewAskUserTool() }},
		{"Ls", func() tools.FuncTool { return tools.NewLsTool() }},
		{"CollectResults", func() tools.FuncTool { return tools.NewCollectResultsTool() }},
		{"TaskList", func() tools.FuncTool { return tools.NewTaskListTool() }},
		{"TaskGet", func() tools.FuncTool { return tools.NewTaskGetTool() }},
		{"TaskUpdate", func() tools.FuncTool { return tools.NewTaskUpdateTool() }},
		{"SubAgent", func() tools.FuncTool { return tools.NewSubAgentTool(rt.spawnSubAgent) }},
		{"AgentTalk", func() tools.FuncTool { return tools.NewAgentTalkTool(rt.agentTalk) }},
	}
	for _, b := range bundled {
		if t := b.factory(); t != nil {
			_ = rt.toolReg.Register(t)
		}
	}
	if tools.IsWindowsPlatform() {
		if ps := tools.NewPowerShellTool(); ps != nil {
			_ = rt.toolReg.Register(ps)
		}
	}
}

// registerDefaultHooks registers the default set of loop and tool hooks.
// Called automatically by NewRuntime during initialization.
//
// Loop hooks (executed in priority order per iteration):
//   - LoopLoggerHook (45): LLM call logging
//   - ConvergenceHook (49): Irrecoverable error detection → Abort
//   - MemoryThoughtHook (50): RAG context injection (prepended if mem set)
//
// Tool hooks (executed in priority order per tool call):
//   - PermissionHook (41): Permission chain evaluation → Deny + Abort
//   - FileModifyHook (42): File backup/before Write/FileEdit (provider may be nil)
//   - ToolLoggerHook (46): Tool execution logging
func (rt *Runtime) registerDefaultHooks() {
	rt.loopHooks = append(rt.loopHooks, loop.Defaults(rt.logger)...)
	if rt.mem != nil {
		rt.loopHooks = append([]hooks.LoopHook{loop.NewMemoryThoughtHook(rt.mem)}, rt.loopHooks...)
	}

	var permStore rule.PermissionRuleStore
	if rt.ruleReg != nil {
		if ps, ok := rt.ruleReg.(rule.PermissionRuleStore); ok {
			permStore = ps
		}
	}
	defaultHooks := action.Defaults(permStore, rt.skillReg, rt.logger, rt.fileModifyTracker)

	// Capture FileModifyHook reference for late binding via WithFileModifyTracker.
	rt.fileModifyHook = nil
	for _, h := range defaultHooks {
		if fmh, ok := h.(*action.FileModifyHook); ok {
			rt.fileModifyHook = fmh
			break
		}
	}

	rt.toolHooks = append(rt.toolHooks, defaultHooks...)
}

// ── AgentTalk: synchronous inter-agent communication ──────────────────────────

// agentTalk implements AgentTalkFunc for synchronous inter-agent communication.
// The target agent runs in its own independent session (session isolation principle),
// avoiding the SubAgent + AgentTalk deadlock where a parent is blocked on CollectResults.
//
// Design decisions:
//   - Creates a fresh session per call using the provided sessionID
//   - Target agent runs via Runtime.Ask() — same ThinkLoop as any other agent
//   - Independent ThinkLoop means the target agent can use tools, do its own work
//   - Session isolation: target agent's messages are completely separate from caller's session
//
// The sessionID parameter enables conversation continuity:
//   - New sessionID = start a fresh conversation with the target agent
//   - Reused sessionID = continue an existing conversation thread
//     (Session caching for continuity is a future enhancement — current impl creates fresh sessions)
func (rt *Runtime) agentTalk(ctx context.Context, to, sessionID, message string) (string, error) {
	if rt.agentReg != nil {
		if cfg := rt.agentReg.Get(to); cfg == nil {
			return "", fmt.Errorf("agent %q not found in registry", to)
		}
	}

	sess := session.NewSession(sessionID, to)
	rt.logger.Info("agent talk started",
		"to", to,
		"session_id", sessionID,
	)

	result, err := rt.Ask(to, message, sess).Run()
	if err != nil {
		return "", fmt.Errorf("agent talk to %q: %w", to, err)
	}

	return result.Answer, nil
}

// ── SubAgent: async task delegation ────────────────────────────────────────────

// spawnSubAgent implements SpawnFunc for creating and running sub-agents.
// Sub-agents run in the background via an independent ThinkLoop, and their results
// are collected later via CollectResultsTool.
//
// Design decisions mirror agentTalk but with key differences:
//   - Creates a unique session per spawn (no session reuse — one-shot task)
//   - Runs via Runtime.Ask() — same ThinkLoop as the main agent
//   - Independent session = full isolation from the parent's context
func (rt *Runtime) spawnSubAgent(ctx context.Context, agentName, task string) (string, error) {
	if rt.agentReg != nil {
		if cfg := rt.agentReg.Get(agentName); cfg == nil {
			return "", fmt.Errorf("agent %q not found in registry", agentName)
		}
	}

	sess := session.NewSession(
		fmt.Sprintf("subagent-%s-%d", agentName, time.Now().UnixNano()),
		agentName,
	)
	rt.logger.Info("sub-agent spawn started",
		"agent_name", agentName,
	)

	result, err := rt.Ask(agentName, task, sess).Run()
	if err != nil {
		return "", fmt.Errorf("sub-agent %q: %w", agentName, err)
	}

	return result.Answer, nil
}

// ── Entry point ─────────────────────────────────────────────────────────────

// Ask creates an AskBuilder for initiating a conversation with an agent.
// This is the primary entry point for interacting with the Runtime's thinking loop.
//
// Ask uses the Builder pattern to allow flexible configuration before execution.
// The returned AskBuilder can be customized with event handlers, then executed
// to run the full ReAct (Reasoning + Acting) loop.
//
// # Parameters
//
//   - agentName: String identifier for the agent configuration to use.
//     Must match a name registered in the AgentRegistry. The agent config
//     defines the role, description, and introduction used in system prompts.
//     Example values: "coder", "analyst", "assistant", "researcher"
//
//   - question: The user's question or instruction to the agent. This will be
//     appended to the session as a user message and sent to the LLM.
//     Can be any natural language query, code request, or task description.
//     Example: "What files are in this directory?", "Explain this function",
//     "Refactor this code to use generics", "Search the web for..."
//
//   - s: A Session instance that maintains conversation history and state.
//     The session handles message storage, compaction, and provides context
//     window management. Each Ask call appends messages to this session.
//     Create sessions with session.New() or session.NewWithConfig().
//
// # Return Value
//
// *AskBuilder: A builder that can be configured and then executed.
//
//	Call builder.Execute() to run the thinking loop and get results.
//	Call builder.OnEvent() to register event handlers for real-time updates.
//
// # Execution Flow
//
// When Execute() is called on the returned builder:
//
//  1. Append user question to session
//  2. Build system prompt from agent config + registries + session state
//  3. For each iteration (up to MaxTurns):
//     a. Run BeforeLLM hooks (can modify/abort)
//     b. Assemble messages (system + history + user)
//     c. Stream LLM response (content + thinking + tool calls)
//     d. Run AfterLLM hooks (can modify/abort)
//     e. If no tool calls → return answer (termination)
//     f. If tool calls → execute tools with hook chain
//     g. Append tool results to session → next iteration
//  4. Return RunResult with answer, usage, duration, termination reason
//
// # Event Handling
//
// Register event handlers to receive real-time updates:
//
//	result, err := rt.Ask("agent", "question", sess).
//	    OnEvent(events.ContentDelta, func(data any) {
//	        fmt.Print(data.(string))  // Stream content to stdout
//	    }).
//	    OnEvent(events.ToolExecStart, func(data any) {
//	        log.Printf("Tool executing: %v", data)
//	    }).
//	    Execute()
//
// Available event types: ContentDelta, ThinkingDelta, ToolUseDelta,
// ToolExecStart, ToolExecEnd, CycleEnd, FinalAnswer, Error, etc.
//
// # Example: Basic Usage
//
//	sess := session.New(session.Config{
//	    SessionDir:  "./sessions",
//	    ProjectDir:  ".",
//	    AgentName:   "coder",
//	})
//
//	result, err := rt.Ask("coder", "List all Go files", sess).Execute()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println(result.Answer)
//
// # Example: With Event Streaming
//
//	builder := rt.Ask("researcher", "Find recent papers on LLMs", sess)
//	builder.OnEvent(events.ContentDelta, func(data any) {
//	    ws.Send(data.(string))  // Stream to WebSocket client
//	})
//	result, err := builder.Execute()
//
// # Cancellation
//
// The AskBuilder includes a context.Context that can be cancelled:
//
//	builder := rt.Ask("agent", "long task", sess)
//	go func() {
//	    time.Sleep(30 * time.Second)
//	    builder.Cancel()  // Cancel after 30 seconds
//	}()
//	result, err := builder.Execute()
func (rt *Runtime) Ask(agentName, question string, s *session.Session) *AskBuilder {
	ctx, cancel := context.WithCancel(context.Background())
	return &AskBuilder{
		ctx:       ctx,
		cancel:    cancel,
		agentName: agentName,
		question:  question,
		session:   s,
		runtime:   rt,
		onEvent:   make(map[events.ReactEventType][]func(any)),
	}
}

// ── ThinkingLoop ────────────────────────────────────────────────────────────

// exec runs the full ThinkingLoop for the AskBuilder.
// This is the core execution engine that implements the ReAct (Reasoning + Acting) pattern.
//
// # Architecture
//
// exec replaces the old Reactor.Run() with a cleaner separation:
//   - Session handles message storage and compaction (sliding window)
//   - Runtime (this method) handles system prompt building, LLM calls, and tool execution
//   - Hooks are integrated into the loop at key points (BeforeLLM, AfterLLM, Tool)
//   - Event bus provides real-time observability
//
// # Execution Flow
//
//  1. Initialize event bus and wire up event handlers from builder
//  2. Set session compaction handler for token limit management
//  3. Create tool executor with event emission and logging
//  4. Determine max iterations from model config (default: 20)
//  5. Append user question to session
//  6. Build tool definitions (stable across iterations)
//  7. For each iteration:
//     a. Check context cancellation
//     b. Build system prompts from registries + session state
//     c. Execute BeforeLLM hooks (can abort/modify)
//     d. Assemble messages (system sections + history + user question)
//     e. Stream LLM response with full configuration
//     f. Parse streaming events (content, thinking, tool calls, errors)
//     g. Execute AfterLLM hooks (can abort/modify)
//     h. Persist assistant message to session
//     i. If no tool calls → emit FinalAnswer and return
//     j. If tool calls → execute tools with hook chain
//     k. Persist tool results to session → next iteration
//  8. On max iterations: return error with termination reason
//
// # Termination Conditions
//
// The loop terminates when:
//   - Context is cancelled (user-initiated or timeout)
//   - A hook aborts the loop (BeforeLLM or AfterLLM returns terminal result)
//   - LLM returns no tool calls (agent has final answer)
//   - LLM returns an error (network, auth, rate limit)
//   - Max iterations reached (default 20, configurable via ModelConfig.MaxTurns)
//
// # Thread Safety
//
// This method is NOT safe for concurrent calls on the same Runtime instance.
// Each call should use its own Runtime or be externally synchronized.
func (rt *Runtime) exec(b *AskBuilder) {
	ctx := b.ctx
	sid := b.session.ID()
	logger := rt.logger

	logger.Info("exec started", "session", sid, "agent", b.agentName, "model", rt.model.Name)

	// Event bus
	eb := events.NewEventBus()
	_, cancel := eb.SubscribeFiltered(func(ev events.ReactEvent) bool {
		if handlers, ok := b.onEvent[ev.Type]; ok {
			for _, h := range handlers {
				h(ev.Data)
			}
		}
		return false
	})
	defer cancel()

	emit := func(typ events.ReactEventType, data any) {
		eb.Emit(events.NewReactEvent(sid, "", "", typ, data))
	}
	conversationID := session.NewRecordID()
	var totalUsage = session.TokenUsage{Timestamp: time.Now()}
	defer func() {
		emit(events.ExecutionSummary, events.ExecutionSummaryData{
			TotalIterations:   b.resultIterations,
			TotalDuration:     b.resultDuration,
			TokensUsed:        totalUsage,
			TerminationReason: b.resultTerminationReason,
		})
	}()

	// Wire session compaction handler to emit compaction events
	b.session.SetCompactionHandler(func(ev session.CompactionEvent) {
		emit(events.Compaction, events.CompactionData{
			SessionID:      sid,
			MessagesSlid:   ev.MessagesSlid,
			RemainingAfter: ev.RemainingAfter,
			WindowSize:     ev.WindowSize,
		})
	})

	// Pre-load session metadata (projectDir, cursor, messages) from store
	// before creating the tool executor, so that WithProjectDirExecutor receives
	// the correct project directory from the persisted session metadata.
	b.session.Current()

	// Tool executor
	toolExec := tools.NewToolExecutor(rt.toolReg,
		tools.WithEventEmitter(func(ev events.ReactEvent) { eb.Emit(ev) }),
		tools.WithLogger(logger),
		tools.WithExecutorSessionID(b.session.ID()),
		tools.WithProjectDirExecutor(b.session.ProjectDir()),
		tools.WithSessionDirExecutor(b.session.SessionDir()),
		tools.WithPermissionChecker(tools.NewFileBoundaryChecker(
			b.session.ProjectDir(),
			b.session.SessionDir(),
		)),
	)

	maxIter := rt.model.MaxTurns
	if maxIter <= 0 {
		maxIter = 20
	}

	// Append user message to session
	b.session.Append(ctx, session.Message{
		Role: "user", Content: b.question, Timestamp: time.Now().Unix(),
	})
	// Build tool definitions once (stable across iterations)
	toolDefs := rt.buildToolDefinitions()

	start := time.Now()
	var lastIteration int
	var prevToolResults []hooks.ToolResult

	setIterResult := func(iter int) {
		b.resultIterations = iter + 1
		b.resultDuration = time.Since(start)
		b.resultUsage = totalUsage
	}

	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			b.resultErr = err
			b.resultTerminationReason = "cancelled"
			setIterResult(iter)
			return
		}

		// Build system prompt sections from Runtime's registries
		systemSections := rt.buildSystemPrompts(sid, b.session)

		// Get current conversation window
		window := b.session.Current()
		logger.Info("session.Current() window", "session", sid, "iter", iter, "msg_count", len(window), "total_chars", func() int {
			total := 0
			for _, m := range window {
				total += len(m.Content)
			}
			return total
		}())

		// ── Loop Hooks BeforeLLM (with panic recovery) ──
		callInput := &hooks.CallInput{
			SessionID:            sid,
			SystemPromptSections: systemSections,
			UserMessage:          b.question,
			History:              window,
			Tools:                toolDefs,
		}
		if hr := rt.execBeforeLLMHooks(sid, iter, callInput, emit); hr.IsTerminal() {
			if hr.Error != nil {
				b.resultErr = hr.Error
				b.resultTerminationReason = "hook_error"
				setIterResult(iter)
				return
			}
			b.resultAnswer = hr.AbortReason
			b.resultTerminationReason = "hook_abort"
			setIterResult(iter)
			logger.Info("loop aborted by hook", "reason", hr.AbortReason)
			return
		}

		// Assemble messages (hooks may have modified systemSections, e.g. MemoryThoughtHook)
		windowHasQuestion := false
		for _, m := range window {
			if m.Role == "user" && m.Content == b.question {
				windowHasQuestion = true
				break
			}
		}
		question := ""
		if !windowHasQuestion {
			question = b.question
		}
		msgs := rt.assembleMessages(callInput.SystemPromptSections, window, question)

		// ── Stream LLM ──
		client := gochat.Client().Config(
			gochat.WithAPIKey(rt.model.APIKey),
			gochat.WithBaseURL(rt.model.BaseURL),
			gochat.WithTimeout(4*time.Minute),
		).WithContext(ctx)
		builder := client.
			Messages(msgs...).
			Model(rt.model.Name).
			Temperature(rt.model.Temperature).
			MaxTokens(int(rt.model.MaxTokens)).
			EnableThinking(true).
			ParallelToolCalls(true).
			ToolChoice("auto")
		if rt.model.TopP > 0 {
			builder = builder.TopP(rt.model.TopP)
		}
		if rt.model.TopK > 0 {
			builder = builder.TopK(int(rt.model.TopK))
		}
		if rt.model.RepetitionPenalty != 0 {
			// RepetitionPenalty maps to gochat's PresencePenalty
			builder = builder.PresencePenalty(rt.model.RepetitionPenalty)
		}
		if rt.model.FrequencyPenalty != 0 {
			builder = builder.FrequencyPenalty(rt.model.FrequencyPenalty)
		}
		if len(toolDefs) > 0 {
			builder = builder.Tools(toolDefs...)
		}

		logger.Info("llm stream started", "session", sid, "iter", iter)

		stream, err := builder.GetStream()
		if err != nil {
			logger.Error("llm GetStream failed", err, "session", sid, "iter", iter)
			emit(events.LLMTimeout, events.LLMTimeoutData{
				SessionID: sid,
				Timeout:   4 * time.Minute,
				Elapsed:   time.Since(start),
				Error:     err.Error(),
			})
			b.resultErr = fmt.Errorf("llm stream failed: %w", err)
			b.resultTerminationReason = "llm_error"
			setIterResult(iter)
			return
		}

		// Streaming loop
		var contentBuf, reasoningBuf strings.Builder
		var streamToolCalls []gochatcore.ToolCall
		var finishReason string
		var streamErr error

		for stream.Next() {
			ev := stream.Event()
			switch ev.Type {
			case gochatcore.EventContent:
				contentBuf.WriteString(ev.Content)
				emit(events.ContentDelta, ev.Content)
			case gochatcore.EventThinking:
				reasoningBuf.WriteString(ev.Content)
				emit(events.ThinkingDelta, ev.Content)
			case gochatcore.EventToolCall:
				for _, delta := range ev.ToolCallDeltas {
					emit(events.ToolUseDelta, events.ToolUseDeltaData{
						Index:     delta.Index,
						ID:        delta.ID,
						Name:      delta.Name,
						Arguments: delta.Arguments,
					})
				}
			case gochatcore.EventError:
				streamErr = ev.Err
			case gochatcore.EventDone:
				finishReason = ev.FinishReason
			}
		}
		stream.Close()

		if streamErr != nil {
			emit(events.LLMTimeout, events.LLMTimeoutData{
				SessionID: sid,
				Timeout:   4 * time.Minute,
				Elapsed:   time.Since(start),
				Error:     streamErr.Error(),
			})
			b.resultErr = streamErr
			b.resultTerminationReason = "llm_error"
			setIterResult(iter)
			return
		}

		// Collect assembled tool calls from stream
		streamToolCalls = stream.ToolCalls()

		emit(events.ThinkingDone, nil)

		// Record token usage from stream
		fmt.Printf("[SSE-TRACE L4] About to call stream.Usage(), session=%s, iter=%d\n", sid, iter)
		u := stream.Usage()
		if u != nil {
			fmt.Printf("[SSE-TRACE L4] stream.Usage() NON-NIL: prompt=%d, completion=%d, total=%d, session=%s\n",
				u.PromptTokens, u.CompletionTokens, u.TotalTokens, sid)
			callUsage := session.TokenUsage{
				Timestamp:    time.Now(),
				InputTokens:  u.PromptTokens,
				OutputTokens: u.CompletionTokens,
				TotalTokens:  u.TotalTokens,
			}
			if u.PromptTokensDetails != nil {
				callUsage.CachedTokens = u.PromptTokensDetails.CachedTokens
			}
			if u.CompletionTokensDetails != nil {
				callUsage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
			}

			// Build and persist TokenUsageRecord with five-dimension grouping keys
			record := session.TokenUsageRecord{
				ID:               session.NewRecordID(),
				SessionID:        sid,
				ConversationID:   conversationID,
				ModelName:        rt.model.Name,
				ProviderName:     rt.model.Provider,
				AgentName:        b.agentName,
				PromptTokens:     callUsage.InputTokens,
				CompletionTokens: callUsage.OutputTokens,
				CachedTokens:     callUsage.CachedTokens,
				ReasoningTokens:  callUsage.ReasoningTokens,
				TotalTokens:      callUsage.TotalTokens,
				Timestamp:        time.Now(),
			}
			if err := rt.tokenUsageStore.Append(ctx, record); err != nil {
				logger.Error("token usage store Append failed", err, "session", sid)
				fmt.Printf("[SSE-TRACE L4] Append FAILED: %v, session=%s\n", err, sid)
			} else {
				fmt.Printf("[SSE-TRACE L4] Append OK: prompt=%d, completion=%d, total=%d, cached=%d, session=%s\n",
					callUsage.InputTokens, callUsage.OutputTokens, callUsage.TotalTokens, callUsage.CachedTokens, sid)
			}
			totalUsage.InputTokens += callUsage.InputTokens
			totalUsage.OutputTokens += callUsage.OutputTokens
			totalUsage.TotalTokens += callUsage.TotalTokens
			totalUsage.CachedTokens += callUsage.CachedTokens
			totalUsage.Timestamp = time.Now()
			logger.Info("token usage recorded", "session", sid, "iter", iter, "input", u.PromptTokens, "output", u.CompletionTokens)
		} else {
			logger.Warn("stream.Usage() returned nil — provider may not support streaming token usage", "session", sid, "iter", iter, "model", rt.model.Name)
			fmt.Printf("[SSE-TRACE L4] stream.Usage() returned NIL! session=%s, iter=%d, model=%s\n", sid, iter, rt.model.Name)
		}

		// Build LLM response for hooks from streaming data
		content := contentBuf.String()
		reasoning := reasoningBuf.String()
		usageCopy := totalUsage
		llmResp := &hooks.LLMResponse{
			Content:      content,
			Reasoning:    reasoning,
			FinishReason: finishReason,
			ToolCalls:    parseToolInvocations(streamToolCalls),
			TokenUsage:   &usageCopy,
		}

		// ── Loop Hooks AfterLLM (with panic recovery) ──
		if hr := rt.execAfterLLMHooks(sid, iter, llmResp, prevToolResults, emit); hr.IsTerminal() {
			if hr.Error != nil {
				b.resultErr = hr.Error
				b.resultTerminationReason = "hook_error"
				setIterResult(iter)
				return
			}
			llmResp.AbortReason = hr.AbortReason
		}

		// Handle abort from AfterLLM hook
		if llmResp.AbortReason != "" {
			emit(events.FinalAnswer, llmResp.Content)
			b.resultAnswer = llmResp.Content
			b.resultTerminationReason = "hook_abort"
			setIterResult(iter)
			return
		}

		// Persist assistant message (content + reasoning + tool_calls)
		assistantMsg := session.Message{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoningBuf.String(),
			Timestamp:        time.Now().Unix(),
		}
		for _, tc := range streamToolCalls {
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, session.ToolCall{
				ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
			})
		}
		b.session.Append(ctx, assistantMsg)

		lastIteration = iter + 1
		emit(events.CycleEnd, events.CycleInfo{
			Iteration: lastIteration, Duration: time.Since(start),
		})
		// ── No tool calls → answer complete ──
		if len(streamToolCalls) == 0 {
			answer := content
			if answer == "" && reasoning != "" {
				answer = reasoning
			}
			switch finishReason {
			case "length", "max_tokens":
				b.resultTerminationReason = "max_tokens"
			case "content_filter":
				b.resultTerminationReason = "content_filtered"
			default:
				b.resultTerminationReason = "completed"
			}
			emit(events.FinalAnswer, answer)
			b.resultAnswer = answer
			b.resultIterations = lastIteration
			b.resultDuration = time.Since(start)
			b.resultUsage = totalUsage
			return
		}

		// ── Execute tools with hooks ──
		invocs := parseToolInvocations(streamToolCalls)

		toolResults := rt.executeTools(ctx, sid, invocs, emit, toolExec)

		prevToolResults = toolResults

		// Persist tool results (full content, not truncated)
		for _, tr := range toolResults {
			var content string
			if tr.Error != "" {
				content = fmt.Sprintf("[%s] error: %s", tr.ToolName, tr.Error)
			} else if tr.Result != "" {
				content = tr.Result
			} else {
				content = fmt.Sprintf("[%s] returned: (empty result)", tr.ToolName)
			}
			logger.Info("tool result persisted", "session", sid, "tool", tr.ToolName, "content_len", len(content))
			b.session.Append(ctx, session.Message{
				Role: "tool", Content: content, Timestamp: time.Now().Unix(),
				ToolCallID: tr.ToolCallID,
			})
		}
	}

	// Max iterations reached - emit event (NOT an error)
	// This is a normal boundary condition, not a failure.
	// The UI should display a friendly suggestion to the user.
	emit(events.MaxTurnsReached, events.MaxTurnsReachedData{
		TurnsCompleted: lastIteration,
		MaxTurns:       maxIter,
		Suggestion: fmt.Sprintf(
			"已达到最大思考轮次 (%d/%d)。任务可能需要更详细的指令或分步完成。你可以发送\"继续\"让 AI 基于当前进度继续，或者提供更具体的指导。",
			lastIteration, maxIter),
	})
	b.resultTerminationReason = "max_iterations"
	b.resultIterations = lastIteration
	b.resultDuration = time.Since(start)
	b.resultUsage = totalUsage
}

// ── Hook execution helpers (with panic recovery + Abort notification) ────────

// execBeforeLLMHooks executes all registered LoopHook.BeforeLLM methods in sequence.
// This is called before each LLM call in the thinking loop, allowing hooks to:
//   - Inspect and modify system prompts, user message, history, or tool definitions
//   - Abort the loop by returning a terminal HookResult
//   - Add context from external sources (e.g., memory retrieval)
//
// Parameters:
//   - sid: Session ID for logging and hook context
//   - iter: Current iteration number (0-based)
//   - input: CallInput containing current state (system prompts, user msg, history, tools)
//   - emit: Function to emit events for real-time monitoring
//
// Returns:
//   - HookResult: Empty if all hooks passed; terminal result if a hook aborted
//
// Safety: Includes panic recovery to prevent one bad hook from crashing the entire loop.
func (rt *Runtime) execBeforeLLMHooks(sid string, iter int, input *hooks.CallInput, emit func(events.ReactEventType, any)) (result hooks.HookResult) {
	defer func() {
		if p := recover(); p != nil {
			rt.logger.Error("loop hook panic", fmt.Errorf("%v", p))
			emit(events.Error, fmt.Sprintf("loop hook panic: %v", p))
			result = hooks.HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range rt.loopHooks {
		hr := h.BeforeLLM(sid, iter, input)
		if hr.IsTerminal() {
			rt.notifyLoopAbort(sid, hr.AbortReason)
			return hr
		}
	}
	return hooks.HookResult{}
}

// execAfterLLMHooks executes all registered LoopHook.AfterLLM methods in sequence.
// This is called after each LLM response, allowing hooks to:
//   - Inspect and modify the LLM response (content, reasoning, tool calls)
//   - Abort the loop by returning a terminal HookResult
//   - Perform post-processing like content filtering or logging
//
// Parameters:
//   - sid: Session ID for logging and hook context
//   - iter: Current iteration number (0-based)
//   - resp: LLMResponse containing the model's response (content, reasoning, tool calls, usage)
//   - toolResults: Tool results from previous iteration (empty on first iteration)
//   - emit: Function to emit events for real-time monitoring
//
// Returns:
//   - HookResult: Empty if all hooks passed; terminal result if a hook aborted
//
// Safety: Includes panic recovery to prevent one bad hook from crashing the entire loop.
func (rt *Runtime) execAfterLLMHooks(sid string, iter int, resp *hooks.LLMResponse, toolResults []hooks.ToolResult, emit func(events.ReactEventType, any)) (result hooks.HookResult) {
	defer func() {
		if p := recover(); p != nil {
			rt.logger.Error("loop hook panic", fmt.Errorf("%v", p))
			emit(events.Error, fmt.Sprintf("loop hook panic: %v", p))
			result = hooks.HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range rt.loopHooks {
		hr := h.AfterLLM(sid, iter, resp, toolResults)
		if hr.IsTerminal() {
			rt.notifyLoopAbort(sid, hr.AbortReason)
			return hr
		}
	}
	return hooks.HookResult{}
}

// notifyLoopAbort notifies all registered hooks that the loop is being aborted.
// Calls Abort() on each hook in reverse registration order (LIFO),
// allowing hooks to perform cleanup in the opposite order of their setup.
//
// Parameters:
//   - sessionID: Session ID for hook context
//   - reason: Reason string explaining why the loop was aborted
//
// This is called when:
//   - A BeforeLLM or AfterLLM hook returns a terminal result with abort reason
//   - The loop needs to terminate early due to external conditions
func (rt *Runtime) notifyLoopAbort(sessionID string, reason string) {
	for i := len(rt.loopHooks) - 1; i >= 0; i-- {
		rt.loopHooks[i].Abort(sessionID, reason)
	}
}

// ── System Prompt Building ──────────────────────────────────────────────────

// buildSystemPrompts constructs the system prompt sections from the Runtime's
// registries and session state. This replaces the old Reactor's Prompt struct.
//
// The system prompt is built as multiple message sections to allow for:
//   - Clear separation of concerns (identity, skills, rules, etc.)
//   - KV cache optimization (static vs dynamic boundary)
//   - Selective modification by hooks
//
// # Prompt Sections (in order):
//
//  1. Identity: Agent name, role, description, introduction (from AgentRegistry)
//  2. Skills Catalog: Available skills and their descriptions (from SkillRegistry)
//  3. Behavioral Rules: Default rules + custom rules (from RuleRegistry)
//  4. Tool Usage Guidelines: How to use tools effectively
//  5. Tone & Style: Communication style instructions
//  6. Environment Info: Session ID, working directories, platform info
//  7. System Reminders: Important operational reminders
//  8. Dynamic Boundary: Marker for KV cache split point
//  9. Output Efficiency: Instructions for concise output
//
// Parameters:
//   - sessionID: Current session identifier for environment info
//   - s: Session instance for retrieving agent name and directory info
//
// Returns:
//   - []gochatcore.Message: Array of system messages forming the complete prompt
func (rt *Runtime) buildSystemPrompts(sessionID string, s *session.Session) []gochatcore.Message {
	var msgs []gochatcore.Message

	// 1. Identity — from AgentRegistry by agent name
	if rt.agentReg != nil {
		cfg := rt.agentReg.Get(s.AgentName())
		if cfg != nil {
			msgs = append(msgs, gochatcore.NewSystemMessage(
				buildIdentity(cfg.Name, cfg.Role, cfg.Description, cfg.Introduction)))
		}
	}

	// 2. Skills catalog
	if rt.skillReg != nil {
		if skills := rt.skillReg.ListSkills(); len(skills) > 0 {
			if catalog := buildSkillsCatalog(skills); catalog != "" {
				msgs = append(msgs, gochatcore.NewSystemMessage(catalog))
			}
		}
	}

	// 3. Behavioral rules
	rules := defaultBehavioralRules()
	if rt.ruleReg != nil {
		if custom := rt.ruleReg.FormatPromptSection(); custom != "" {
			rules += "\n" + custom
		}
	}
	msgs = append(msgs, gochatcore.NewSystemMessage("## Behavioral Rules\n"+rules))

	// 4. Tool usage guidelines
	msgs = append(msgs, gochatcore.NewSystemMessage(buildToolUsageGuidelines()))

	// 5. Tone & style
	msgs = append(msgs, gochatcore.NewSystemMessage(buildToneAndStyle()))

	// 6. Environment info
	msgs = append(msgs, gochatcore.NewSystemMessage(buildEnvironmentInfo(environmentInfoParams{
		SessionID:  sessionID,
		SessionDir: s.SessionDir(),
		ProjectDir: s.ProjectDir(),
	})))

	// 7. System reminders
	msgs = append(msgs, gochatcore.NewSystemMessage(buildSystemReminders()))

	// 8. Dynamic boundary (KV cache split point)
	msgs = append(msgs, gochatcore.NewSystemMessage("__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__"))

	// 9. Output efficiency (dynamic section)
	msgs = append(msgs, gochatcore.NewSystemMessage(buildOutputEfficiency()))

	return msgs
}

// ── Message Assembly ────────────────────────────────────────────────────────

// assembleMessages builds the complete message sequence for the LLM API call.
// Combines system prompts, conversation history, and user question into the
// correct order expected by the LLM provider.
//
// # Message Order:
//
// 1. System sections (from buildSystemPrompts) - multiple system messages
// 2. Conversation history (micro-compacted, max 2 consecutive same-role msgs)
//   - Converted from session.Message to gochatcore.Message format
//   - Preserves tool calls and tool call IDs in assistant/tool messages
//
// 3. User question (if not already present as last message in history)
//
// # Compaction
//
// History is compacted using session.MicroCompact() which merges consecutive
// messages of the same role to reduce token count while preserving information.
//
// Parameters:
//   - systemSections: System prompt sections from buildSystemPrompts()
//   - history: Conversation history from session.Current()
//   - question: User's current question (may be empty if already in history)
//
// Returns:
//   - []gochatcore.Message: Complete message sequence ready for LLM API call
func (rt *Runtime) assembleMessages(systemSections []gochatcore.Message, history []session.Message, question string) []gochatcore.Message {
	var msgs []gochatcore.Message
	msgs = append(msgs, systemSections...)

	// Compact and convert session messages to gochat messages
	window := session.MicroCompact(history, 2)
	for _, m := range window {
		switch m.Role {
		case "system":
			msgs = append(msgs, gochatcore.NewSystemMessage(m.Content))
		case "user":
			msgs = append(msgs, gochatcore.NewUserMessage(m.Content))
		case "assistant":
			msg := gochatcore.NewTextMessage("assistant", m.Content)
			msg.ReasoningContent = m.ReasoningContent
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, gochatcore.ToolCall{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			msgs = append(msgs, msg)
		case "tool":
			toolMsg := gochatcore.NewTextMessage("tool", m.Content)
			toolMsg.ToolCallID = m.ToolCallID
			msgs = append(msgs, toolMsg)
		default:
			msgs = append(msgs, gochatcore.NewTextMessage(m.Role, m.Content))
		}
	}

	// User message (if not already the last message in window)
	if question != "" {
		if len(window) == 0 || window[len(window)-1].Role != "user" || window[len(window)-1].Content != question {
			msgs = append(msgs, gochatcore.NewUserMessage(question))
		}
	}

	return msgs
}

// ── Tool Execution ──────────────────────────────────────────────────────────

// executeTools runs multiple tool calls with the ToolHook chain, supporting both
// async (concurrent) and sync (sequential) execution modes.
//
// # Execution Strategy
//
// For each tool invocation:
//   - If tool.IsAsync == true: Execute concurrently in a goroutine with asyncTimeout
//   - If tool.IsAsync == false: Execute synchronously in sequence with syncTimeout
//
// Async tools run in parallel using WaitGroup for coordination. Results are collected
// into a slice maintaining original invocation order (by index).
//
// # Hook Integration
//
// Each tool execution goes through:
// 1. ToolHook.Before chain (all hooks, can abort/deny)
// 2. Actual tool execution via ToolExecutor
// 3. ToolHook.After chain (all hooks, can modify result)
//
// Parameters:
//   - ctx: Context for cancellation and timeout propagation
//   - sessionID: Session ID for logging and hook context
//   - invocs: Tool invocations to execute (from LLM response)
//   - emit: Event emission function for real-time monitoring
//   - toolExec: ToolExecutor instance to use for execution
//
// Returns:
//   - []hooks.ToolResult: Results for each invocation, same order as input
func (rt *Runtime) executeTools(
	ctx context.Context,
	sessionID string,
	invocs []hooks.ToolCallInvocation,
	emit func(events.ReactEventType, any),
	toolExec tools.ToolExecutor,
) []hooks.ToolResult {
	if len(invocs) == 0 {
		return nil
	}

	results := make([]hooks.ToolResult, len(invocs))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, inv := range invocs {
		if rt.isToolAsync(inv.Name) {
			wg.Add(1)
			i, inv := i, inv
			go func() {
				defer wg.Done()
				tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.asyncTimeout)
				mu.Lock()
				results[i] = tr
				mu.Unlock()
			}()
		} else {
			tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.syncTimeout)
			results[i] = tr
		}
	}

	wg.Wait()
	return results
}

// isToolAsync checks if a tool should be executed asynchronously (concurrently).
// Looks up the tool in the registry and returns its IsAsync flag.
//
// Parameters:
//   - name: Tool name to look up
//
// Returns:
//   - bool: true if tool exists and is marked as async, false otherwise
func (rt *Runtime) isToolAsync(name string) bool {
	if t, ok := rt.toolReg.Get(name); ok {
		return t.Info().IsAsync
	}
	return false
}

// executeSingleTool executes a single tool invocation with full hook chain support.
// This is the core tool execution function called by executeTools for each invocation.
//
// # Execution Flow:
//
// 1. ToolHook.Before chain:
//   - Each hook can inspect and approve/deny the execution
//   - If any hook aborts, returns failed result immediately
//
// 2. Emit ToolExecStart event
// 3. Create timeout context (asyncTimeout or syncTimeout)
// 4. Execute tool via ToolExecutor.Execute()
// 5. Build ToolResult from execution result or error
// 6. ToolHook.After chain:
//   - Each hook can inspect and modify the result
//   - If any hook aborts, replaces result with failure
//
// 7. Emit ToolExecEnd event with final result
// 8. Return ToolResult
//
// Parameters:
//   - ctx: Parent context for cancellation propagation
//   - sessionID: Session ID for logging and hooks
//   - inv: ToolCallInvocation containing name, ID, and arguments
//   - emit: Event emission function
//   - toolExec: ToolExecutor to use
//   - timeout: Maximum execution duration for this tool call
//
// Returns:
//   - hooks.ToolResult: Complete execution result including success/failure, output, error, duration
func (rt *Runtime) executeSingleTool(
	ctx context.Context,
	sessionID string,
	inv hooks.ToolCallInvocation,
	emit func(events.ReactEventType, any),
	toolExec tools.ToolExecutor,
	timeout time.Duration,
) hooks.ToolResult {
	start := time.Now()

	// ToolHook.Before
	for _, h := range rt.toolHooks {
		hr := h.Before(sessionID, inv.Name, inv.Arguments)
		if hr.Abort {
			emit(events.PermissionDenied, hr.AbortReason)
			return failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
		}
		if hr.Error != nil {
			emit(events.PermissionDenied, hr.Error.Error())
			return failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
		}
	}

	emit(events.ToolExecStart, events.ToolExecStartData{
		ToolName: inv.Name,
		Params:   inv.Arguments,
	})
	execCtx, execCancel := context.WithTimeout(ctx, timeout)
	defer execCancel()

	execResult, execErr := toolExec.Execute(execCtx, inv.Name, inv.Arguments)
	tr := buildToolResult(inv, execResult, execErr, start)

	// ToolHook.After
	for _, h := range rt.toolHooks {
		hr := h.After(&tr)
		if hr.Abort {
			tr = failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
			break
		}
		if hr.Error != nil {
			tr = failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
			break
		}
	}

	emit(events.ToolExecEnd, events.ToolExecEndData{
		ToolName:   tr.ToolName,
		ToolCallID: tr.ToolCallID,
		Duration:   time.Since(start),
		Success:    tr.Success,
		Result:     tr.Result,
		Error:      tr.Error,
	})
	return tr
}

// failedToolResult creates a failed ToolResult for tool execution errors or aborts.
// Used when hooks deny execution, tools fail, or timeouts occur.
//
// Parameters:
//   - toolName: Name of the tool that failed
//   - toolCallID: ID of the tool call from LLM response
//   - errMsg: Human-readable error message
//   - start: Timestamp when execution started (for duration calculation)
//
// Returns:
//   - hooks.ToolResult: Failed result with Success=false, Error set, Duration calculated
func failedToolResult(toolName, toolCallID, errMsg string, start time.Time) hooks.ToolResult {
	return hooks.ToolResult{
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Success:    false,
		Error:      errMsg,
		Duration:   time.Since(start),
	}
}

// buildToolResult constructs a ToolResult from tool execution output or error.
// Handles both successful executions (with result data) and failures.
//
// Parameters:
//   - inv: Original tool invocation (name, ID, arguments)
//   - execResult: Execution result from ToolExecutor (may be nil on error)
//   - execErr: Error from tool execution (nil on success)
//   - start: Start timestamp for duration calculation
//
// Returns:
//   - hooks.ToolResult: Complete result with success status, output/error, metadata, duration
func buildToolResult(inv hooks.ToolCallInvocation, execResult *tools.ToolExecutionResult, execErr error, start time.Time) hooks.ToolResult {
	tr := hooks.ToolResult{
		ToolName:   inv.Name,
		ToolCallID: inv.ID,
		Duration:   time.Since(start),
	}
	if execErr != nil {
		tr.Error = execErr.Error()
		tr.Success = false
	} else if execResult != nil {
		tr.Result = execResult.Result
		tr.Metadata = execResult.Metadata
		tr.Duration = execResult.Duration
		tr.Success = execResult.Error == nil
		if execResult.Error != nil {
			tr.Error = execResult.Error.Error()
		}
	}
	return tr
}

// ── LLM Response Parsing ────────────────────────────────────────────────────

// parseToolInvocations converts LLM tool call objects into internal hook format.
// Parses JSON arguments strings into parameter maps for each tool call.
//
// # Input Processing
//
// Filters out invalid calls (missing ID or name). For argument parsing:
//   - Valid JSON: Unmarshal into map[string]any
//   - Invalid JSON: Store as {"raw_args": original_string} to preserve data
//
// Parameters:
//   - calls: Tool calls from gochat streaming response
//
// Returns:
//   - []hooks.ToolCallInvocation: Parsed invocations ready for execution, or nil if empty
func parseToolInvocations(calls []gochatcore.ToolCall) []hooks.ToolCallInvocation {
	if len(calls) == 0 {
		return nil
	}
	invocs := make([]hooks.ToolCallInvocation, 0, len(calls))
	for _, tc := range calls {
		if tc.ID == "" || tc.Name == "" {
			continue
		}
		var params map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
				params = map[string]any{"raw_args": tc.Arguments}
			}
		}
		invocs = append(invocs, hooks.ToolCallInvocation{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: params,
		})
	}
	return invocs
}

// ── Tool Definition Building ────────────────────────────────────────────────

// buildToolDefinitions converts all registered tools into LLM API tool definition format.
// Called once at the start of exec() since tool definitions are stable across iterations.
//
// Each tool's Info() is converted to gochatcore.Tool with:
//   - Name: Tool identifier
//   - Description: Human-readable description for LLM
//   - Parameters: JSON Schema object built from Parameter slice
//
// Returns:
//   - []gochatcore.Tool: Array of tool definitions for LLM API, or nil if no tools registered
func (rt *Runtime) buildToolDefinitions() []gochatcore.Tool {
	if rt.toolReg == nil {
		return nil
	}
	allTools := rt.toolReg.All()
	if len(allTools) == 0 {
		return nil
	}
	out := make([]gochatcore.Tool, 0, len(allTools))
	for _, t := range allTools {
		info := t.Info()
		out = append(out, gochatcore.Tool{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  buildParamSchema(info.Parameters),
		})
	}
	return out
}

// buildParamSchema converts a Parameter slice into JSON Schema format for LLM tool definitions.
// Builds a proper JSON Schema "object" type with properties for each parameter.
//
// # Schema Structure
//
//	{
//	  "type": "object",
//	  "properties": {
//	    "param1": {"type": "string", "description": "...", "enum": [...]},
//	    "param2": {"type": "integer", "description": "..."}
//	  }
//	}
//
// Parameters:
//   - params: Tool parameter definitions from tools.Info().Parameters
//
// Returns:
//   - json.RawMessage: Marshaled JSON Schema, or empty object schema if params is nil/empty
func buildParamSchema(params []tools.Parameter) json.RawMessage {
	if len(params) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	props := schema["properties"].(map[string]any)
	for _, p := range params {
		prop := map[string]any{
			"type":        paramTypeToJSONType(p.Type),
			"description": p.Description,
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[p.Name] = prop
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return b
}

// paramTypeToJSONType maps Go/tool parameter type strings to JSON Schema type names.
// Handles common aliases and defaults unknown types to "string".
//
// # Type Mapping
//
//   - "integer", "int", "int64", "int32" → "integer"
//   - "number", "float64", "float32" → "number"
//   - "boolean", "bool" → "boolean"
//   - "array", "[]string", "[]int" → "array"
//   - "object", "map" → "object"
//   - Everything else → "string" (default)
//
// Parameters:
//   - t: Type string from tool parameter definition
//
// Returns:
//   - string: JSON Schema compatible type name
func paramTypeToJSONType(t string) string {
	switch t {
	case "integer", "int", "int64", "int32":
		return "integer"
	case "number", "float64", "float32":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "array", "[]string", "[]int":
		return "array"
	case "object", "map":
		return "object"
	default:
		return "string"
	}
}

// ── RegistryHub accessors ─────────────────────────────────────────────────────

// Logger returns the Runtime's structured logger instance.
// The logger is used throughout the runtime for debug, info, warning, and error messages.
// Default implementation outputs JSON-formatted logs to stdout.
//
// Returns:
//   - logging.Logger: The configured logger instance. Never nil (defaults to DefaultLogger).
//
// Example:
//
//	logger := rt.Logger()
//	logger.Info("Starting Ask", "agent", agentName, "question", question)
func (rt *Runtime) Logger() logging.Logger { return rt.logger }

// ToolRegistry returns the Runtime's tool registry containing all registered tools.
// The tool registry manages available tools that the LLM can invoke during execution.
// Built-in tools are registered automatically by NewRuntime; additional tools
// can be registered via RegisterTool() or WithToolRegistry().
//
// Returns:
//   - tools.ToolRegistry: The tool registry instance. Never nil.
//
// Example:
//
//	reg := rt.ToolRegistry()
//	if tool, ok := reg.Get("Bash"); ok {
//	    fmt.Printf("Bash tool: %s\n", tool.Info().Description)
//	}
func (rt *Runtime) ToolRegistry() tools.ToolRegistry { return rt.toolReg }

// SkillRegistry returns the Runtime's skill registry for managing agent capabilities.
// Skills define high-level capabilities exposed to the agent in system prompts.
// Unlike tools (which are function-calling), skills describe what an agent CAN do.
//
// Returns:
//   - skill.SkillRegistry: The skill registry instance. May be nil if not configured.
//
// Example:
//
//	skills := rt.SkillRegistry()
//	if skills != nil {
//	    for _, s := range skills.ListSkills() {
//	        fmt.Printf("Skill: %s - %s\n", s.Name, s.Description)
//	    }
//	}
func (rt *Runtime) SkillRegistry() skill.SkillRegistry { return rt.skillReg }

// RuleRegistry returns the Runtime's rule registry for behavioral constraints.
// Rules define how the agent should behave, what it should avoid, and any
// operational boundaries. Rules are included in system prompts.
//
// Returns:
//   - rule.RuleRegistry: The rule registry instance. May be nil if not configured.
//
// Example:
//
//	rules := rt.RuleRegistry()
//	if rules != nil {
//	    promptSection := rules.FormatPromptSection()
//	    fmt.Println(promptSection)
//	}
func (rt *Runtime) RuleRegistry() rule.RuleRegistry { return rt.ruleReg }

// ProviderRegistry returns the Runtime's LLM provider registry.
// Provider configurations can be used for multi-provider setups or fallback logic.
//
// Returns:
//   - config.ProviderRegistry: The provider registry instance. May be nil.
func (rt *Runtime) ProviderRegistry() config.ProviderRegistry { return rt.providerReg }

// ToolExecutor returns the Runtime's tool execution engine.
// The executor handles tool invocation with hook support, timeout management,
// and event emission. It wraps the ToolRegistry with execution logic.
//
// Returns:
//   - tools.ToolExecutor: The tool executor instance.
//
// Note: For executing tools in custom code, prefer using this executor rather than
// calling tools directly, as it includes hook chain and error handling.
func (rt *Runtime) ToolExecutor() tools.ToolExecutor {
	return rt.toolExec
}

// AgentRegistry returns the Runtime's agent configuration registry.
// Agent configs define roles, descriptions, and introductions used to build
// identity sections in system prompts.
//
// Returns:
//   - *config.AgentRegistry: The agent registry pointer. May be nil.
//
// Example:
//
//	agents := rt.AgentRegistry()
//	if agents != nil {
//	    cfg := agents.Get("coder")
//	    if cfg != nil {
//	        fmt.Printf("Role: %s\n", cfg.Role)
//	    }
//	}
func (rt *Runtime) AgentRegistry() *config.AgentRegistry { return rt.agentReg }

// WithFileModifyTracker sets the file modification tracker provider for this Runtime.
// When set, a FileModifyHook is automatically registered in the default tool hooks
// to backup files before Write/FileEdit tools execute.
//
// The provider function receives a sessionID and returns the session's TrackModify
// function (or false if tracking is not available for that session).
//
// Example:
//
//	rt.WithFileModifyTracker(func(sessionID string) (action.TrackFunc, bool) {
//	    sess := getSessionByID(sessionID)
//	    if sess == nil { return nil, false }
//	    return sess.TrackModify, true
//	})
func (rt *Runtime) WithFileModifyTracker(provider action.TrackerProvider) {
	rt.fileModifyTracker = provider
	if rt.fileModifyHook != nil {
		rt.fileModifyHook.SetProvider(provider)
	}
}

// RegisterTool adds a new tool to the Runtime's tool registry.
// The tool will be available for the LLM to invoke in subsequent Ask calls.
// Tool definitions are sent to the LLM as part of the API request.
//
// Parameters:
//   - tool: A FuncTool instance implementing the tool logic.
//     Must have valid Name, Description, and Parameters in its Info().
//
// Returns:
//   - error: Non-nil if tool registration fails (e.g., duplicate name, invalid config).
//
// Example:
//
//	customTool := tools.NewFuncTool(tools.Info{
//	    Name:        "Weather",
//	    Description: "Get current weather for a city",
//	    Parameters: []tools.Parameter{
//	        {Name: "city", Type: "string", Description: "City name"},
//	    },
//	}, func(ctx context.Context, params map[string]any) (string, error) {
//	    city := params["city"].(string)
//	    return getWeather(city), nil
//	})
//
//	if err := rt.RegisterTool(customTool); err != nil {
//	    log.Fatalf("Failed to register tool: %v", err)
//	}
func (rt *Runtime) RegisterTool(tool tools.FuncTool) error {
	return rt.toolReg.Register(tool)
}

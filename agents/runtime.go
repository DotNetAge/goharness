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
}

// RunResult holds execution results from a single Ask.
type RunResult struct {
	Answer            string
	TokenUsage        session.TokenUsage
	Duration          time.Duration
	Iterations        int
	TerminationReason string
}

// NewRuntime creates a Runtime with the given options.
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
	r.toolExec = tools.NewToolExecutor(r.toolReg)
	r.registerDefaultTools()
	return r
}

// ── Default tool registration ───────────────────────────────────────────────

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

// ── Entry point ─────────────────────────────────────────────────────────────

// Ask creates an AskBuilder for the given agent name, question, and session.
func (rt *Runtime) Ask(agentName, question string, s *session.Session) *AskBuilder {
	return &AskBuilder{
		ctx:       context.Background(),
		agentName: agentName,
		question:  question,
		session:   s,
		runtime:   rt,
		onEvent:   make(map[events.ReactEventType][]func(any)),
	}
}

// ── ThinkingLoop ────────────────────────────────────────────────────────────

// exec runs the full ThinkingLoop for the AskBuilder.
// It replicates the Reactor's Run() functionality using the new architecture:
//   - Session handles message storage and compaction
//   - Runtime handles system prompt building, LLM calls, and tool execution
//   - Hooks are integrated into the loop
func (rt *Runtime) exec(b *AskBuilder) {
	ctx := b.ctx
	sid := b.session.ID()
	logger := rt.logger

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
	defer func() {
		emit(events.ExecutionSummary, events.ExecutionSummaryData{
			TotalIterations:   b.resultIterations,
			TotalDuration:     b.resultDuration,
			TokensUsed:        b.resultUsage,
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

	// Tool executor
	toolExec := tools.NewToolExecutor(rt.toolReg,
		tools.WithEventEmitter(func(ev events.ReactEvent) { eb.Emit(ev) }),
		tools.WithLogger(logger),
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
	var totalUsage = session.TokenUsage{Timestamp: time.Now()}
	var lastIteration int

	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			b.resultErr = err
			b.resultUsage = totalUsage
			return
		}

		// Build system prompt sections from Runtime's registries
		systemSections := rt.buildSystemPrompts(sid, b.session)

		// Get current conversation window
		window := b.session.Current()

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
				b.resultUsage = totalUsage
				return
			}
			b.resultAnswer = hr.AbortReason
			b.resultTerminationReason = "hook_abort"
			b.resultIterations = iter + 1
			b.resultDuration = time.Since(start)
			b.resultUsage = totalUsage
			logger.Info("loop aborted by hook", "reason", hr.AbortReason)
			return
		}

		// Assemble messages (hooks may have modified systemSections, e.g. MemoryThoughtHook)
		msgs := rt.assembleMessages(callInput.SystemPromptSections, window, b.question)

		// ── Stream LLM ──
		client := gochat.Client().Config(
			gochat.WithAPIKey(rt.model.APIKey),
			gochat.WithBaseURL(rt.model.BaseURL),
			gochat.WithTimeout(4*time.Minute),
		)
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

		stream, err := builder.GetStream()
		if err != nil {
			emit(events.LLMTimeout, events.LLMTimeoutData{
				SessionID: sid,
				Timeout:   4 * time.Minute,
				Elapsed:   time.Since(start),
				Error:     err.Error(),
			})
			b.resultErr = fmt.Errorf("llm stream failed: %w", err)
			b.resultUsage = totalUsage
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
			b.resultUsage = totalUsage
			return
		}

		// Collect assembled tool calls from stream
		streamToolCalls = stream.ToolCalls()

		emit(events.ThinkingDone, nil)

		// Record token usage from stream
		if u := stream.Usage(); u != nil {
			totalUsage.InputTokens += u.PromptTokens
			totalUsage.OutputTokens += u.CompletionTokens
			totalUsage.TotalTokens += u.TotalTokens
		}

		// Build LLM response for hooks from streaming data
		content := contentBuf.String()
		reasoning := reasoningBuf.String()
		llmResp := &hooks.LLMResponse{
			Content:      content,
			Reasoning:    reasoning,
			FinishReason: finishReason,
			ToolCalls:    parseToolInvocations(streamToolCalls),
		}

		// ── Loop Hooks AfterLLM (with panic recovery) ──
		if hr := rt.execAfterLLMHooks(sid, iter, llmResp, emit); hr.IsTerminal() {
			if hr.Error != nil {
				b.resultErr = hr.Error
				b.resultUsage = totalUsage
				return
			}
			llmResp.AbortReason = hr.AbortReason
		}

		// Handle abort from AfterLLM hook
		if llmResp.AbortReason != "" {
			emit(events.FinalAnswer, llmResp.Content)
			b.resultAnswer = llmResp.Content
			b.resultTerminationReason = "hook_abort"
			b.resultIterations = iter + 1
			b.resultDuration = time.Since(start)
			b.resultUsage = totalUsage
			return
		}

		// Persist assistant message
		assistantMsg := session.Message{
			Role: "assistant", Content: content, Timestamp: time.Now().Unix(),
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

		// Persist tool results
		for _, tr := range toolResults {
			content := hooks.ToolResultSummary(tr)
			b.session.Append(ctx, session.Message{
				Role: "tool", Content: content, Timestamp: time.Now().Unix(),
				ToolCallID: tr.ToolCallID,
			})
		}
	}

	// Max iterations reached
	b.resultErr = fmt.Errorf("max iterations (%d) reached", maxIter)
	b.resultIterations = lastIteration
	b.resultDuration = time.Since(start)
	b.resultUsage = totalUsage
}

// ── Hook execution helpers (with panic recovery + Abort notification) ────────

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

func (rt *Runtime) execAfterLLMHooks(sid string, iter int, resp *hooks.LLMResponse, emit func(events.ReactEventType, any)) (result hooks.HookResult) {
	defer func() {
		if p := recover(); p != nil {
			rt.logger.Error("loop hook panic", fmt.Errorf("%v", p))
			emit(events.Error, fmt.Sprintf("loop hook panic: %v", p))
			result = hooks.HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range rt.loopHooks {
		hr := h.AfterLLM(sid, iter, resp, nil)
		if hr.IsTerminal() {
			rt.notifyLoopAbort(sid, hr.AbortReason)
			return hr
		}
	}
	return hooks.HookResult{}
}

func (rt *Runtime) notifyLoopAbort(sessionID string, reason string) {
	for i := len(rt.loopHooks) - 1; i >= 0; i-- {
		rt.loopHooks[i].Abort(sessionID, reason)
	}
}

// ── System Prompt Building ──────────────────────────────────────────────────

// buildSystemPrompts constructs the system prompt sections from the Runtime's
// registries and session state. This replaces the old Reactor's Prompt struct.
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

// assembleMessages builds the complete message sequence for the LLM call.
// Order: system sections → conversation history → user message.
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

// executeTools runs tool calls with the ToolHook chain, supporting async/sync execution.
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

func (rt *Runtime) isToolAsync(name string) bool {
	if t, ok := rt.toolReg.Get(name); ok {
		return t.Info().IsAsync
	}
	return false
}

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

func failedToolResult(toolName, toolCallID, errMsg string, start time.Time) hooks.ToolResult {
	return hooks.ToolResult{
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Success:    false,
		Error:      errMsg,
		Duration:   time.Since(start),
	}
}

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

func parseToolInvocations(calls []gochatcore.ToolCall) []hooks.ToolCallInvocation {
	if len(calls) == 0 {
		return nil
	}
	invocs := make([]hooks.ToolCallInvocation, len(calls))
	for i, tc := range calls {
		var params map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
				params = map[string]any{"raw_args": tc.Arguments}
			}
		}
		invocs[i] = hooks.ToolCallInvocation{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: params,
		}
	}
	return invocs
}

// ── Tool Definition Building ────────────────────────────────────────────────

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
	b, _ := json.Marshal(schema)
	return b
}

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

func (rt *Runtime) Logger() logging.Logger          { return rt.logger }
func (rt *Runtime) ToolRegistry() tools.ToolRegistry { return rt.toolReg }
func (rt *Runtime) SkillRegistry() skill.SkillRegistry { return rt.skillReg }
func (rt *Runtime) RuleRegistry() rule.RuleRegistry   { return rt.ruleReg }
func (rt *Runtime) ProviderRegistry() config.ProviderRegistry { return rt.providerReg }

func (rt *Runtime) ToolExecutor() tools.ToolExecutor {
	return rt.toolExec
}

func (rt *Runtime) AgentRegistry() *config.AgentRegistry { return rt.agentReg }

func (rt *Runtime) RegisterTool(tool tools.FuncTool) error {
	return rt.toolReg.Register(tool)
}

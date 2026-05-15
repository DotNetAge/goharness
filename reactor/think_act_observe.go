package reactor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

// Think executes a single thinking phase with full-schema tools.
// No L1 routing — the LLM decides tool vs answer in one call.
// The System Prompt and Instructions remain stable across rounds;
// direction is steered via tool result footers.
func (r *Reactor) Think(ctx *ReactContext) (int, error) {
	thinkStart := time.Now()
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1
	r.getLogger().Info("think start",
		"session_id", sessionID,
		"iteration", iter,
		"model", r.config.Model,
		"input_preview", truncate(ctx.Input, 80),
	)

	// Pre-Think: restore any previously offloaded results from disk
	r.restoreOffloadedResults(ctx)

	// Use cached LLM tool definitions — rebuilt only when RegisterTool is called
	llmTools := r.getLLMTools()

	sessionDir := ""
	if r.fileStore != nil {
		sessionDir = r.fileStore.GetSessionPath(sessionID)
	}
	// Fallback to Reactor's sessionDir if fileStore didn't provide one
	if sessionDir == "" && r.sessionDir != "" {
		sessionDir = r.sessionDir
	}

	// Build system prompt sections using the centralized Prompt
	// Inject projectDir from Agent/Reactor setup (Design-time safety guarantee)
	var sections []gochatcore.Message
	if r.prompt != nil {
		sections = r.prompt.ToSectionedMessages(sessionID, sessionDir, r.projectDir)
	}

	callInput := CallInput{
		SessionID:            sessionID,
		SystemPromptSections: sections,
		UserMessage:          ctx.Input,
		History:              ctx.ConversationHistory,
		Tools:                llmTools,
	}

	var contentBuf strings.Builder

	// 防御性检查：确保 LLMCaller 已初始化
	if r.llmCaller == nil {
		r.getLogger().Error("llm caller is nil, cannot execute think",
			fmt.Errorf("LLMCaller not initialized in Reactor"),
			"session_id", sessionID,
			"iteration", iter,
		)
		return 0, fmt.Errorf("llm caller not initialized")
	}

	result := r.llmCaller.CallStream(ctx.Ctx(), callInput,
		func(chunk string) {
			contentBuf.WriteString(chunk)
		},
		func(thinkingChunk string) {
			ctx.EmitEvent(core.ThinkingDelta, thinkingChunk)
		},
	)

	// LLM 调用本身失败（网络、认证、超时等），直接返回错误，避免将错误文本送入 ParseThinkResponse。
	if result.Error != nil {
		r.getLogger().Error("llm call failed in think", result.Error,
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(thinkStart).Milliseconds(),
		)
		return int(result.TokenUsage.InputTokens), fmt.Errorf("llm call failed: %w", result.Error)
	}

	content := contentBuf.String()
	if content == "" {
		content = result.Content
	}

	var thought *Thought
	if len(result.ToolCalls) > 0 {
		thought = nativeToolCallsToThought(result.ToolCalls)
		r.getLogger().Info("think done",
			"session_id", sessionID,
			"iteration", iter,
			"decision", thought.Decision,
			"elapsed_ms", time.Since(thinkStart).Milliseconds(),
			"input_tokens", result.TokenUsage.InputTokens,
			"output_tokens", result.TokenUsage.OutputTokens,
			"tool_calls", len(thought.ToolCalls),
		)
	} else {
		var parseErr error
		thought, parseErr = ParseThinkResponse(content, r.getLogger())
		if parseErr != nil {
			r.getLogger().Error("think parse failed", parseErr,
				"session_id", sessionID,
				"iteration", iter,
				"elapsed_ms", time.Since(thinkStart).Milliseconds(),
				"raw_preview", truncate(content, 100),
				"content_length", len(content),
			)
			return int(result.TokenUsage.InputTokens), fmt.Errorf("think parse failed: %w", parseErr)
		}
		r.getLogger().Info("think done",
			"session_id", sessionID,
			"iteration", iter,
			"decision", thought.Decision,
			"elapsed_ms", time.Since(thinkStart).Milliseconds(),
			"input_tokens", result.TokenUsage.InputTokens,
			"output_tokens", result.TokenUsage.OutputTokens,
			"is_final_answer", thought.IsFinal && thought.Decision == DecisionAnswer,
		)
	}

	ctx.LastThought = thought
	return int(result.TokenUsage.InputTokens), nil
}

// nativeToolCallsToThought converts native gochat ToolCalls to the Thought format used by
// the Act phase. This bridges native function calling (non-streaming) with the
// Thought-based execution pipeline.
//
// Native tool calls are converted to DecisionAct with the ToolCalls map populated.
// The tool name → parameter map structure matches what executeToolCalls expects.
func nativeToolCallsToThought(tcs []gochatcore.ToolCall) *Thought {
	if len(tcs) == 0 {
		return nil
	}

	toolCalls := make(map[string]map[string]any, len(tcs))
	toolCallIDs := make(map[string]string, len(tcs))
	for _, tc := range tcs {
		var params map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
				params = map[string]any{"raw_args": tc.Arguments}
			}
		}
		toolCalls[tc.Name] = params
		toolCallIDs[tc.Name] = tc.ID
	}

	return &Thought{
		Decision:    DecisionAct,
		ToolCalls:   toolCalls,
		ToolCallIDs: toolCallIDs,
		Reasoning:   "LLM returned native tool calls",
	}
}

// Act executes the decision from the Think phase.
func (r *Reactor) Act(ctx *ReactContext) error {
	thought := ctx.LastThought
	if thought == nil {
		return fmt.Errorf("act called without a thought")
	}

	start := time.Now()
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1

	switch thought.Decision {
	case DecisionAnswer:
		r.getLogger().Info("act answer",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(start).Milliseconds(),
			"answer_preview", truncate(thought.FinalAnswer, 80),
		)
		ctx.LastAction = &Action{
			Type:   ActionTypeAnswer,
			Result: coalesce(thought.FinalAnswer, thought.Reasoning),
		}
		return nil

	case DecisionClarify:
		q := thought.ClarificationQuestion
		if q == "" {
			q = "Could you provide more details so I can better assist you?"
		}
		r.getLogger().Info("act clarify",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(start).Milliseconds(),
			"question_preview", truncate(q, 80),
		)
		ctx.LastAction = &Action{Type: ActionTypeClarify, Result: q}
		return nil

	case DecisionAct:
		r.getLogger().Info("act toolcalls",
			"session_id", sessionID,
			"iteration", iter,
			"tool_count", len(thought.ToolCalls),
		)
		return r.executeToolCalls(ctx, thought, start)

	case DecisionDelegate:
		r.getLogger().Info("act delegate",
			"session_id", sessionID,
			"iteration", iter,
			"delegate_target", thought.DelegateTarget,
		)
		return r.executeDelegate(ctx, thought, start)

	default:
		r.getLogger().Info("act default",
			"session_id", sessionID,
			"iteration", iter,
			"decision", thought.Decision,
		)
		ctx.LastAction = &Action{
			Type:   ActionTypeAnswer,
			Result: coalesce(thought.FinalAnswer, thought.Reasoning),
		}
		return nil
	}
}

// executeToolCalls executes tool calls in two phases:
//
//  1. Sync tools (IsAsync=false) execute SERIALLY, one at a time.
//     Each must complete before the next starts. Results are collected in order.
//
//  2. Async tools (IsAsync=true) execute in PARALLEL, launched in goroutines.
//     Each returns immediately with {task_id, status: "running"}.
//
// This ensures deterministic behavior for sync tools (e.g., read_file, grep)
// while allowing long-running async tools (e.g., web_search, bash) to run concurrently.
func (r *Reactor) executeToolCalls(ctx *ReactContext, thought *Thought, start time.Time) error {
	type toolCall struct {
		name   string
		params map[string]any
	}
	var calls []toolCall

	if len(thought.ToolCalls) > 0 {
		for name, params := range thought.ToolCalls {
			calls = append(calls, toolCall{name, params})
		}
	} else if thought.ActionTarget != "" {
		calls = append(calls, toolCall{thought.ActionTarget, thought.ActionParams})
	}

	if len(calls) == 0 {
		ctx.LastAction = &Action{
			Type:   ActionTypeAnswer,
			Result: coalesce(thought.FinalAnswer, "Sorry, I cannot determine which tool to use for your request."),
		}
		return nil
	}

	// Separate sync and async tool calls
	var syncCalls, asyncCalls []toolCall
	for _, c := range calls {
		isAsync := false
		if tool, ok := r.toolRegistry.Get(c.name); ok {
			isAsync = tool.Info().IsAsync
		}
		if isAsync {
			asyncCalls = append(asyncCalls, c)
		} else {
			syncCalls = append(syncCalls, c)
		}
	}

	var action Action
	action.Timestamp = start
	action.Type = ActionTypeToolCall
	var results []string

	// Phase 1: Execute sync tools SERIALLY (one at a time)
	sessionID := r.resolveSessionID(ctx)
	for _, c := range syncCalls {
		toolStart := time.Now()
		r.getLogger().Info("tool start",
			"session_id", sessionID,
			"tool", c.name,
			"params_preview", truncate(fmt.Sprintf("%v", c.params), 120),
		)
		ctx.EmitEvent(core.ActionProgress, fmt.Sprintf("正在执行: %s", c.name))
		res, err := r.toolExecutor.Execute(ctx.Ctx(), c.name, c.params)
		toolElapsed := time.Since(toolStart)
		if err != nil {
			r.getLogger().Error("tool error", err,
				"session_id", sessionID,
				"tool", c.name,
				"elapsed_ms", toolElapsed.Milliseconds(),
			)
			action.Error = err
			action.ErrorMsg = err.Error()
			results = append(results, fmt.Sprintf("[%s] error: %s", c.name, err.Error()))
		} else if res.Interaction != nil {
			r.getLogger().Info("tool interaction",
				"session_id", sessionID,
				"tool", c.name,
				"elapsed_ms", toolElapsed.Milliseconds(),
			)
			answer, interactErr := r.interactionHandler.HandleInteraction(ctx.Ctx(), res.Interaction)
			if interactErr != nil {
				results = append(results, fmt.Sprintf("[%s] interaction error: %s", c.name, interactErr.Error()))
			} else {
				results = append(results, fmt.Sprintf("[%s] %s", c.name, answer))
			}
			if res.Duration > action.Duration {
				action.Duration = res.Duration
			}
		} else {
			resultSize := len(res.Result)
			r.getLogger().Info("tool done",
				"session_id", sessionID,
				"tool", c.name,
				"elapsed_ms", toolElapsed.Milliseconds(),
				"result_size", resultSize,
				"success", true,
			)
			results = append(results, fmt.Sprintf("[%s] %s", c.name, res.Result))
			if res.Duration > action.Duration {
				action.Duration = res.Duration
			}
		}
		ctx.EmitEvent(core.ActionProgress, fmt.Sprintf("%s 已完成", c.name))
	}

	// Phase 2: Execute async tools in PARALLEL (all launched at once)
	if len(asyncCalls) > 0 {
		type asyncResult struct {
			name string
			id   string
			err  error
		}
		asyncCh := make(chan asyncResult, len(asyncCalls))

		for _, c := range asyncCalls {
			c := c // capture
			go func(toolName string, params map[string]any) {
				asyncCtx, cancel := context.WithTimeout(ctx.Ctx(), 5*time.Minute)
				defer cancel()

				resultCh := make(chan struct{}, 1)
				var execErr error
				go func() {
					defer func() {
						if r := recover(); r != nil {
							execErr = fmt.Errorf("tool %q panicked: %v", toolName, r)
						}
						resultCh <- struct{}{}
					}()
					_, execErr = r.toolExecutor.Execute(asyncCtx, toolName, params)
				}()

				select {
				case <-resultCh:
					taskID := fmt.Sprintf("async-%s-%d", toolName, time.Now().UnixNano())
					asyncCh <- asyncResult{name: toolName, id: taskID, err: execErr}
				case <-asyncCtx.Done():
					taskID := fmt.Sprintf("async-%s-%d", toolName, time.Now().UnixNano())
					asyncCh <- asyncResult{
						name: toolName,
						id:   taskID,
						err:  fmt.Errorf("async tool %q timed out after 5m", toolName),
					}
				}
			}(c.name, c.params)
		}

		for i := 0; i < len(asyncCalls); i++ {
			ar := <-asyncCh
			if ar.err != nil {
				results = append(results, fmt.Sprintf("[%s] error: %s", ar.name, ar.err.Error()))
			} else {
				results = append(results, fmt.Sprintf("[%s] %s", ar.name,
					fmt.Sprintf(`{"task_id": "%s", "status": "running"}`, ar.id)))
			}
		}
	}

	action.Result = strings.Join(results, "\n")
	ctx.LastAction = &action
	return nil
}

func (r *Reactor) executeDelegate(ctx *ReactContext, thought *Thought, start time.Time) error {
	target := thought.DelegateTarget
	prompt := thought.DelegatePrompt

	if target == "" {
		ctx.LastAction = &Action{
			Type:      ActionTypeDelegate,
			Result:    "delegate failed: no target agent specified",
			ErrorMsg:  "no target agent specified",
			Timestamp: start,
		}
		return nil
	}

	if r.SpawnFunc == nil {
		ctx.LastAction = &Action{
			Type:      ActionTypeDelegate,
			Result:    fmt.Sprintf("delegate to %q failed: sub-agent spawning is not configured", target),
			ErrorMsg:  "SpawnFunc not configured",
			Timestamp: start,
		}
		return nil
	}

	result, err := r.SpawnFunc(ctx.Ctx(), target, prompt)
	if err != nil {
		ctx.LastAction = &Action{
			Type:      ActionTypeDelegate,
			Result:    fmt.Sprintf("[delegate:%s] error: %s", target, err.Error()),
			Error:     err,
			ErrorMsg:  err.Error(),
			Timestamp: start,
		}
		return nil
	}

	ctx.LastAction = &Action{
		Type:      ActionTypeDelegate,
		Result:    fmt.Sprintf("[delegate:%s] %s", target, result),
		Timestamp: start,
	}
	return nil
}

func coalesce(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Observe evaluates the result of the Act phase.
// In Executor mode: analyzes tool execution results (existing logic).
// In Coordinator mode: analyzes sub-task completion status, checks if all done.
func (r *Reactor) Observe(ctx *ReactContext) error {
	observeStart := time.Now()
	action := ctx.LastAction
	if action == nil {
		return fmt.Errorf("observe called without an action")
	}
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1

	// ====== Coordinator Mode Branch ======
	if ctx.Mode == ModeCoordinator && ctx.CoordState != nil {
		return r.observeCoordinator(ctx)
	}

	var obs *Observation

	switch action.Type {
	case ActionTypeToolCall:
		if action.Error != nil {
			obs = NewErrorObservation(action.Error, false)
			obs.Insights = []string{fmt.Sprintf("Tool %q execution failed", action.Target)}
			r.getLogger().Warn("observe tool error",
				"session_id", sessionID,
				"iteration", iter,
				"tool", action.Target,
				"elapsed_ms", time.Since(observeStart).Milliseconds(),
				"error", action.Error,
			)
		} else {
			insights := analyzeActionResult(action.Result)
			obs = NewSuccessObservation(action.Result, insights...)
			r.getLogger().Info("observe tool success",
				"session_id", sessionID,
				"iteration", iter,
				"tool", action.Target,
				"elapsed_ms", time.Since(observeStart).Milliseconds(),
				"insights", len(insights),
			)
		}

	case ActionTypeAnswer:
		obs = NewSuccessObservation(action.Result, "direct answer generated")
		r.getLogger().Info("observe answer",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	case ActionTypeClarify:
		obs = NewSuccessObservation(action.Result, "clarification question generated")
		r.getLogger().Info("observe clarify",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	case ActionTypeDelegate:
		obs = NewSuccessObservation(action.Result, "delegation executed")
		r.getLogger().Info("observe delegate",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	default:
		obs = NewSuccessObservation(action.Result)
		r.getLogger().Info("observe default",
			"session_id", sessionID,
			"iteration", iter,
			"action_type", action.Type,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)
	}

	ctx.LastObservation = obs
	return nil
}

// observeCoordinator handles Observe phase when in Coordinator mode.
// Checks sub-task progress, determines if all tasks are done, and produces
// a final answer or continues waiting.
func (r *Reactor) observeCoordinator(ctx *ReactContext) error {
	cs := ctx.CoordState
	if cs == nil || cs.TaskProgress == nil {
		return fmt.Errorf("coordinator mode but no CoordState")
	}

	tp := cs.TaskProgress
	total := tp.Count()
	completed := tp.CompletedCount()
	failed := tp.FailedCount()
	pending := tp.PendingCount()

	r.getLogger().Info("coordinator observe",
		"total", total, "completed", completed, "failed", failed, "pending", pending)

	var obs *Observation

	if tp.AllCompleted() {
		// All tasks done → produce summary answer
		summary := r.buildCoordinatorSummary(cs)
		cs.MarkCompleted()

		// Switch back to Executor mode for next cycle (if any)
		ctx.Mode = ModeExecutor

		obs = NewSuccessObservation(summary,
			fmt.Sprintf("all %d tasks done (%d succeeded, %d failed)", total, completed, failed))
		ctx.LastThought = &Thought{
			Decision:    DecisionAnswer,
			Reasoning:   fmt.Sprintf("Coordination complete. %d/%d tasks succeeded.", completed, total),
			FinalAnswer: summary,
			IsFinal:     true,
		}
	} else if pending > 0 {
		// Still waiting for results
		obs = NewSuccessObservation(tp.Summary(),
			fmt.Sprintf("coordinating: %d/%d complete, %d pending", completed+failed, total, pending))

		// Check lifecycle state — if cancelled/interrupted, stop
		if cs.LifecycleState.IsTerminal() {
			summary := r.buildCoordinatorSummary(cs)
			obs = NewSuccessObservation(summary, fmt.Sprintf("coordination ended: %s", cs.LifecycleState))
			ctx.LastThought = &Thought{
				Decision:    DecisionAnswer,
				FinalAnswer: summary,
				IsFinal:     true,
			}
		}
	} else {
		// All terminal states reached (mix of success/fail)
		summary := r.buildCoordinatorSummary(cs)
		cs.MarkCompleted()
		ctx.Mode = ModeExecutor

		obs = NewSuccessObservation(summary,
			fmt.Sprintf("coordination finished: %d succeeded, %d failed", completed, failed))
		ctx.LastThought = &Thought{
			Decision:    DecisionAnswer,
			Reasoning:   "All sub-tasks reached terminal state",
			FinalAnswer: summary,
			IsFinal:     true,
		}
	}

	ctx.LastObservation = obs
	return nil
}

// buildCoordinatorSummary builds a human-readable summary of all sub-task results.
func (r *Reactor) buildCoordinatorSummary(cs *CoordState) string {
	if cs.TaskProgress == nil {
		return "(no task progress)"
	}

	var sb strings.Builder
	entries := cs.TaskProgress.ListAll()

	sb.WriteString("## Task Coordination Summary\n\n")

	for _, e := range entries {
		statusIcon := map[TaskStatus]string{
			TaskSucceeded:    "[OK]",
			TaskFailed:       "[FAIL]",
			TaskTimedOut:     "[TIMEOUT]",
			TaskCancelled:    "[CANCELLED]",
			TaskRetryPending: "[RETRY]",
		}[e.Status]

		if statusIcon == "" {
			statusIcon = string(e.Status)
		}

		fmt.Fprintf(&sb, "%s **%s** (priority=%d)\n", statusIcon, e.Title, e.Priority)

		if e.Result != nil && e.Result.Content != "" {
			// Truncate very long results
			content := e.Result.Content
			if len(content) > 500 {
				content = content[:500] + "...(truncated)"
			}
			fmt.Fprintf(&sb, "  Result: %s\n", content)
		}
		if e.Error != nil {
			fmt.Fprintf(&sb, "  Error: %v\n", e.Error)
		}
		sb.WriteString("\n")
	}

	// Append aggregated stats
	s := cs.TaskProgress
	fmt.Fprintf(&sb, "---\nTotal: %d | Succeeded: %d | Failed: %d\n",
		s.Count(), s.CompletedCount(), s.FailedCount())

	return sb.String()
}

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

// buildCallInput assembles the CallInput for an LLM call.
// It resolves session ID, builds system prompt sections from Prompt,
// and includes conversation history and tool definitions.
func (r *Reactor) buildCallInput(ctx *ReactContext) *CallInput {
	sessionID := r.resolveSessionID(ctx)
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

	return &CallInput{
		SessionID:            sessionID,
		SystemPromptSections: sections,
		UserMessage:          ctx.Input,
		History:              ctx.ConversationHistory,
		Tools:                llmTools,
	}
}

// Think executes a single thinking phase with full-schema tools.
// No L1 routing — the LLM decides tool vs answer in one call.
// The System Prompt and Instructions remain stable across rounds;
// direction is steered via tool result footers.
//
// Returns (inputTokens, outputTokens, error).
// On success, ctx.LastThought is populated with the parsed Thought.
// On abort (hook), ctx.TerminationReason is set and error is nil.
// On failure, error is non-nil and ctx.LastThought may be partially set.
func (r *Reactor) Think(ctx *ReactContext) (int, int, error) {
	thinkStart := time.Now()
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1

	callInput := r.buildCallInput(ctx)

	// 防御性检查：确保 LLMCaller 已初始化
	if r.llmCaller == nil {
		r.getLogger().Error("llm caller is nil, cannot execute think",
			fmt.Errorf("LLMCaller not initialized in Reactor"),
			"session_id", sessionID,
			"iteration", iter,
		)
		return 0, 0, fmt.Errorf("llm caller not initialized")
	}

	// Thought Before hooks (PreCheck, ThoughtEvent, ThoughtLogger)
	if hr := r.execThoughtHooksBefore(ctx, callInput); hr.IsTerminal() {
		if hr.Error != nil {
			return 0, 0, hr.Error
		}
		ctx.TerminationReason = hr.AbortReason
		ctx.LastThought = &Thought{
			Decision:  DecisionAnswer,
			IsFinal:   true,
			Reasoning: hr.AbortReason,
		}
		return 0, 0, nil
	}

	var contentBuf strings.Builder

	result := r.llmCaller.CallStream(ctx.Ctx(), *callInput,
		func(chunk string) {
			contentBuf.WriteString(chunk)
		},
		func(thinkingChunk string) {
			ctx.EmitEvent(core.ThinkingDelta, thinkingChunk)
		},
	)

	// LLM 调用本身失败（网络、认证、超时等）
	if result.Error != nil {
		r.getLogger().Error("llm call failed in think", result.Error,
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(thinkStart).Milliseconds(),
		)
		if result.TimedOut {
			ctx.EmitEvent(core.LLMTimeout, core.LLMTimeoutData{
				SessionID: sessionID,
				Timeout:   r.llmCaller.streamTimeout,
				Elapsed:   time.Since(thinkStart),
				Error:     result.Error.Error(),
			})
		}
		return int(result.TokenUsage.InputTokens), int(result.TokenUsage.OutputTokens), fmt.Errorf("llm call failed: %w", result.Error)
	}

	content := contentBuf.String()
	if content == "" {
		content = result.Content
	}

	var thought *Thought
	if len(result.ToolCalls) > 0 {
		thought = nativeToolCallsToThought(result.ToolCalls)
	} else {
		var parseErr error
		thought, parseErr = ParseThinkResponse(content, r.getLogger())
		if parseErr != nil {
			r.getLogger().Error("think parse failed", parseErr,
				"session_id", sessionID,
				"iteration", iter,
				"elapsed_ms", time.Since(thinkStart).Milliseconds(),
				"raw_preview", Truncate(content, 100),
				"content_length", len(content),
			)
			return int(result.TokenUsage.InputTokens), int(result.TokenUsage.OutputTokens), fmt.Errorf("think parse failed: %w", parseErr)
		}
	}

	// Set LastThought before After hooks so hooks can reference it
	ctx.LastThought = thought

	// Thought After hooks (ThoughtEvent, ThoughtLogger)
	if hr := r.execThoughtHooksAfter(ctx, thought); hr.IsTerminal() {
		if hr.Error != nil {
			return int(result.TokenUsage.InputTokens), int(result.TokenUsage.OutputTokens), hr.Error
		}
		ctx.TerminationReason = hr.AbortReason
	}

	return int(result.TokenUsage.InputTokens), int(result.TokenUsage.OutputTokens), nil
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
// Based on Thought.Decision, it either:
//   - Generates a direct answer (DecisionAnswer)
//   - Asks a clarification question (DecisionClarify)
//   - Executes tool calls (DecisionAct)
//   - Delegates to a sub-agent (DecisionDelegate)
//
// On success, ctx.LastAction is populated with the execution results.
// Returns error if the decision is unknown or execution fails.
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
			"answer_preview", Truncate(thought.FinalAnswer, 80),
		)
		ctx.LastAction = &Action{
			Results: []ToolResult{{ToolName: "answer", Result: coalesce(thought.FinalAnswer, thought.Reasoning), Success: true}},
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
			"question_preview", Truncate(q, 80),
		)
		ctx.LastAction = &Action{Results: []ToolResult{{ToolName: "clarify", Result: q, Success: true}}}
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
		r.getLogger().Warn("act unknown decision",
			"session_id", sessionID,
			"iteration", iter,
			"decision", thought.Decision,
		)
		return fmt.Errorf("unknown decision type: %s", thought.Decision)
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
	calls := r.parseToolCalls(thought)
	if len(calls) == 0 {
		return r.handleEmptyCalls(ctx, thought)
	}

	syncCalls, asyncCalls := r.partitionByAsync(calls)

	sessionID := r.resolveSessionID(ctx)
	var results []ToolResult

	r.executeSyncTools(ctx, syncCalls, &results)
	r.executeAsyncTools(ctx, asyncCalls, start, sessionID, &results)

	return r.assembleActionResult(ctx, calls, start, results)
}

// toolCall represents a single parsed tool call with its parameters and ID.
type toolCall struct {
	name       string         // Tool name as registered in the tool registry
	params     map[string]any // Parameters from Thought.ToolCalls[name]
	toolCallID string         // Original tool_call_id from LLM response (for OpenAI compatibility)
}

// parseToolCalls converts Thought.ToolCalls map into a slice of toolCall structs.
// Each entry in the map becomes one toolCall with resolved toolCallID.
func (r *Reactor) parseToolCalls(thought *Thought) []toolCall {
	var calls []toolCall
	for name, params := range thought.ToolCalls {
		calls = append(calls, toolCall{
			name:       name,
			params:     params,
			toolCallID: thought.ToolCallIDs[name],
		})
	}
	return calls
}

// handleEmptyCalled handles the case where Thought.Decision is "act" but ToolCalls is empty.
// This can happen if the LLM returns an empty tool_calls array.
// Falls back to DecisionAnswer with a generic error message.
func (r *Reactor) handleEmptyCalls(ctx *ReactContext, thought *Thought) error {
	ctx.LastThought.Decision = DecisionAnswer
	ctx.LastAction = &Action{
		Results: []ToolResult{{ToolName: "answer", Result: coalesce(thought.FinalAnswer, "Sorry, I cannot determine which tool to use for your request."), Success: true}},
	}
	return nil
}

func (r *Reactor) partitionByAsync(calls []toolCall) (syncCalls, asyncCalls []toolCall) {
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
	return
}

func (r *Reactor) executeSyncTools(ctx *ReactContext, syncCalls []toolCall, results *[]ToolResult) {
	for _, c := range syncCalls {
		tr := r.execToolHooksWithAbort(ctx, c)
		*results = append(*results, *tr)
	}
}

func (r *Reactor) executeAsyncTools(ctx *ReactContext, asyncCalls []toolCall, start time.Time, sessionID string, results *[]ToolResult) {
	if len(asyncCalls) == 0 {
		return
	}

	type asyncResult struct {
		name       string
		result     string
		err        error
		toolCallID string
	}
	asyncCh := make(chan asyncResult, len(asyncCalls))

	for _, c := range asyncCalls {
		c := c
		go func(toolName string, params map[string]any, toolCallID string) {
			asyncCtx, cancel := context.WithTimeout(ctx.Ctx(), r.asyncToolTimeout)
			defer cancel()

			ctx.EmitEvent(core.ToolExecStart, core.ToolExecStartData{
				ToolName: toolName,
				Params:   params,
			})

			resultCh := make(chan struct{}, 1)
			var execResult *core.ToolExecutionResult
			var execErr error
			go func() {
				defer func() {
					if r := recover(); r != nil {
						execErr = fmt.Errorf("tool %q panicked: %v", toolName, r)
					}
					resultCh <- struct{}{}
				}()
				execResult, execErr = r.toolExecutor.Execute(asyncCtx, toolName, params)
			}()

			select {
			case <-resultCh:
				// normal completion or panic
			case <-asyncCtx.Done():
				execErr = fmt.Errorf("async tool %q timed out after %v", toolName, r.asyncToolTimeout)
			}

			resultStr := ""
			if execErr == nil && execResult != nil {
				resultStr = execResult.Result
			}

			tr := ToolResult{
				ToolName:   toolName,
				ToolCallID: toolCallID,
				Duration:   time.Since(start),
				Success:    execErr == nil,
				Result:     resultStr,
			}
			if execErr != nil {
				tr.Error = execErr.Error()
			}

			endData := core.ToolExecEndData{
				ToolName:   toolName,
				ToolCallID: toolCallID,
				Duration:   time.Since(start),
				Success:    execErr == nil,
				Result:     resultStr,
			}
			if execErr != nil {
				endData.Error = execErr.Error()
			}
			ctx.EmitEvent(core.ToolExecEnd, endData)

			asyncCh <- asyncResult{name: toolName, result: resultStr, err: execErr, toolCallID: toolCallID}
		}(c.name, c.params, c.toolCallID)
	}

	for remaining := len(asyncCalls); remaining > 0; {
		select {
		case ar := <-asyncCh:
			remaining--
			var tr ToolResult
			tr.ToolName = ar.name
			tr.ToolCallID = ar.toolCallID
			tr.Result = ar.result
			tr.Success = ar.err == nil
			if ar.err != nil {
				tr.Error = ar.err.Error()
			}
			*results = append(*results, tr)
		case <-ctx.Ctx().Done():
			r.getLogger().Warn("context cancelled while waiting for async tools",
				"remaining", remaining)
			remaining = 0
		}
	}
}

func (r *Reactor) assembleActionResult(ctx *ReactContext, calls []toolCall, start time.Time, results []ToolResult) error {
	summary := strings.Join(func() []string {
		var parts []string
		for _, r := range results {
			parts = append(parts, r.ToolResultSummary())
		}
		return parts
	}(), "\n")

	successCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		}
	}

	ctx.EmitEvent(core.ActionEnd, core.ActionEndData{
		TotalTools:   len(calls),
		SuccessCount: successCount,
		FailedCount:  len(calls) - successCount,
		Summary:      summary,
	})

	ctx.LastAction = &Action{
		Timestamp: start,
		Results:   results,
	}
	return nil
}

func (r *Reactor) executeDelegate(ctx *ReactContext, thought *Thought, start time.Time) error {
	target := thought.DelegateTarget
	prompt := thought.DelegatePrompt

	if target == "" {
		ctx.LastAction = &Action{
			Results:   []ToolResult{{ToolName: "delegate", Result: "delegate failed: no target agent specified", Success: false}},
			Timestamp: start,
		}
		return nil
	}

	if r.SpawnFunc == nil {
		ctx.LastAction = &Action{
			Results:   []ToolResult{{ToolName: "delegate", Result: fmt.Sprintf("delegate to %q failed: sub-agent spawning is not configured", target), Success: false}},
			Timestamp: start,
		}
		return nil
	}

	result, err := r.SpawnFunc(ctx.Ctx(), target, prompt)
	if err != nil {
		ctx.LastAction = &Action{
			Results:   []ToolResult{{ToolName: "delegate", Result: fmt.Sprintf("[delegate:%s] error: %s", target, err.Error()), Success: false}},
			Timestamp: start,
		}
		return nil
	}

	ctx.LastAction = &Action{
		Results:   []ToolResult{{ToolName: "delegate", Result: fmt.Sprintf("[delegate:%s] %s", target, result), Success: true}},
		Timestamp: start,
	}
	return nil
}

// Observe evaluates the result of the Act phase.
// It analyzes the Action results and produces an Observation with:
//   - Success/failure status
//   - Result summary for context
//   - Insights for loop detection (e.g., "tools failed", "large result")
//   - ShouldRetry flag for error recovery
//
// After building the Observation, it runs ObservationHook.After chain
// which may modify the Observation or signal termination.
func (r *Reactor) Observe(ctx *ReactContext) error {
	action := ctx.LastAction
	if action == nil {
		return fmt.Errorf("observe called without an action")
	}

	var obs *Observation

	switch ctx.LastThought.Decision {
	case DecisionAct:
		successCount := 0
		var failedTools []string
		for _, tr := range action.Results {
			if tr.Success {
				successCount++
			} else {
				failedTools = append(failedTools, tr.ToolName)
			}
		}

		switch {
		case len(failedTools) > 0:
			errMsg := fmt.Sprintf("tools failed: %s", strings.Join(failedTools, ", "))
			obs = NewErrorObservation(fmt.Errorf("%s", errMsg), false)
			obs.Insights = []string{fmt.Sprintf("%d/%d tools failed: %s", len(failedTools), len(action.Results), errMsg)}
		default:
			insights := analyzeActionResult(action.Summary())
			obs = NewSuccessObservation(action.Summary(), insights...)
		}

	case DecisionAnswer:
		obs = NewSuccessObservation(action.Summary(), "direct answer generated")

	case DecisionClarify:
		obs = NewSuccessObservation(action.Summary(), "clarification question generated")

	case DecisionDelegate:
		obs = NewSuccessObservation(action.Summary(), "delegation executed")
	}

	ctx.LastObservation = obs

	// Observation After hooks (ObservationEvent, ObservationLogger, Convergence)
	if hr := r.execObservationHooksAfter(ctx, obs); hr.IsTerminal() {
		if hr.Error != nil {
			return hr.Error
		}
		ctx.TerminationReason = hr.AbortReason
	}

	return nil
}

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
func (r *Reactor) Think(ctx *ReactContext) (int, int, error) {
	thinkStart := time.Now()
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1
	r.getLogger().Info("think start",
		"session_id", sessionID,
		"iteration", iter,
		"model", r.config.Model,
		"input_preview", truncate(ctx.Input, 80),
	)

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
		return 0, 0, fmt.Errorf("llm caller not initialized")
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
			return int(result.TokenUsage.InputTokens), int(result.TokenUsage.OutputTokens), fmt.Errorf("think parse failed: %w", parseErr)
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
			"question_preview", truncate(q, 80),
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
	}
	return nil
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

	r.emitActionStart(ctx, calls)

	toolCallIDs := thought.ToolCallIDs
	r.executeSyncTools(ctx, syncCalls, sessionID, toolCallIDs, &results)
	r.executeAsyncTools(ctx, asyncCalls, start, sessionID, toolCallIDs, &results)

	return r.assembleActionResult(ctx, calls, start, results)
}

type toolCall struct {
	name   string
	params map[string]any
}

func (r *Reactor) parseToolCalls(thought *Thought) []toolCall {
	var calls []toolCall
	if len(thought.ToolCalls) > 0 {
		for name, params := range thought.ToolCalls {
			calls = append(calls, toolCall{name, params})
		}
	} else if thought.ActionTarget != "" {
		calls = append(calls, toolCall{thought.ActionTarget, thought.ActionParams})
	}
	return calls
}

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

func (r *Reactor) emitActionStart(ctx *ReactContext, calls []toolCall) {
	predictedTokens := ctx.CurrentInputTokens
	if predictedTokens > 0 {
		predictedTokens = int(float64(predictedTokens) * 1.5)
	}
	var toolNames []string
	for _, c := range calls {
		toolNames = append(toolNames, c.name)
	}
	ctx.EmitEvent(core.ActionStart, core.ActionStartData{
		ToolCount:            len(calls),
		ToolNames:            toolNames,
		TotalPredictedTokens: predictedTokens,
		Iteration:            ctx.CurrentIteration,
	})
}

func (r *Reactor) executeSyncTools(ctx *ReactContext, syncCalls []toolCall, sessionID string, toolCallIDs map[string]string, results *[]ToolResult) {
	for _, c := range syncCalls {
		toolStart := time.Now()

		r.getLogger().Info("tool start",
			"session_id", sessionID,
			"tool", c.name,
			"params_preview", truncate(fmt.Sprintf("%v", c.params), 120),
		)

		ctx.EmitEvent(core.ToolExecStart, core.ToolExecStartData{
			ToolName: c.name,
			Params:   c.params,
		})

		res, err := r.toolExecutor.Execute(ctx.Ctx(), c.name, c.params)
		toolElapsed := time.Since(toolStart)

		endData := core.ToolExecEndData{
			ToolName:   c.name,
			ToolCallID: toolCallIDs[c.name],
			Duration:   toolElapsed,
			Success:    true,
		}

		tr := ToolResult{
			ToolName:   c.name,
			ToolCallID: toolCallIDs[c.name],
			Duration:   toolElapsed,
			Success:    true,
		}

		if err != nil {
			r.getLogger().Error("tool error", err,
				"session_id", sessionID,
				"tool", c.name,
				"elapsed_ms", toolElapsed.Milliseconds(),
			)
			endData.Success = false
			endData.Error = err.Error()
			tr.Success = false
			tr.Error = err.Error()
		} else if res.Interaction != nil {
			r.getLogger().Info("tool interaction",
				"session_id", sessionID,
				"tool", c.name,
				"elapsed_ms", toolElapsed.Milliseconds(),
			)
			answer, interactErr := r.interactionHandler.HandleInteraction(ctx.Ctx(), res.Interaction)
			if interactErr != nil {
				endData.Success = false
				endData.Error = interactErr.Error()
				tr.Success = false
				tr.Error = interactErr.Error()
			} else {
				endData.Result = answer
				tr.Result = answer
			}
			if res.Duration > toolElapsed {
				endData.Duration = res.Duration
				tr.Duration = res.Duration
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
			endData.Result = res.Result
			tr.Result = res.Result
		}

		*results = append(*results, tr)
		ctx.EmitEvent(core.ToolExecEnd, endData)
	}
}

func (r *Reactor) executeAsyncTools(ctx *ReactContext, asyncCalls []toolCall, start time.Time, sessionID string, toolCallIDs map[string]string, results *[]ToolResult) {
	if len(asyncCalls) == 0 {
		return
	}

	type asyncResult struct {
		name        string
		result      string
		err         error
		toolCallID  string
	}
	asyncCh := make(chan asyncResult, len(asyncCalls))

	for _, c := range asyncCalls {
		c := c
		go func(toolName string, params map[string]any) {
			asyncCtx, cancel := context.WithTimeout(ctx.Ctx(), 5*time.Minute)
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
				execErr = fmt.Errorf("async tool %q timed out after 5m", toolName)
			}

			resultStr := ""
			if execErr == nil && execResult != nil {
				resultStr = execResult.Result
			}

			tr := ToolResult{
				ToolName:   toolName,
				ToolCallID: toolCallIDs[toolName],
				Duration:   time.Since(start),
				Success:    execErr == nil,
				Result:     resultStr,
			}
			if execErr != nil {
				tr.Error = execErr.Error()
			}

			endData := core.ToolExecEndData{
				ToolName:   toolName,
				ToolCallID: toolCallIDs[toolName],
				Duration:   time.Since(start),
				Success:    execErr == nil,
				Result:     resultStr,
			}
			if execErr != nil {
				endData.Error = execErr.Error()
			}
			ctx.EmitEvent(core.ToolExecEnd, endData)

			asyncCh <- asyncResult{name: toolName, result: resultStr, err: execErr, toolCallID: toolCallIDs[toolName]}
		}(c.name, c.params)
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

	if len(calls) > 1 {
		ctx.EmitEvent(core.ActionProgress, core.ActionProgressData{
			CompletedCount: len(calls),
			TotalCount:     len(calls),
			Status:         "completed",
		})
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

	var obs *Observation

	switch ctx.LastThought.Decision {
	case DecisionAct:
		// Count success/failure from Results, also check action.Error for legacy paths
		successCount := 0
		var failedTools []string
		for _, tr := range action.Results {
			if tr.Success {
				successCount++
			} else {
				failedTools = append(failedTools, tr.ToolName)
			}
		}
		toolNames := make([]string, len(action.Results))
		for i, tr := range action.Results {
			toolNames[i] = tr.ToolName
		}

		switch {
		case len(failedTools) > 0:
			errMsg := fmt.Sprintf("tools failed: %s", strings.Join(failedTools, ", "))
			obs = NewErrorObservation(fmt.Errorf("%s", errMsg), false)
			obs.Insights = []string{fmt.Sprintf("%d/%d tools failed: %s", len(failedTools), len(action.Results), errMsg)}
			r.getLogger().Warn("observe tool error",
				"session_id", sessionID,
				"iteration", iter,
				"tools", toolNames,
				"elapsed_ms", time.Since(observeStart).Milliseconds(),
				"failed", len(failedTools),
				"errors", strings.Join(failedTools, ","),
			)

		default:
			insights := analyzeActionResult(action.Summary())
			obs = NewSuccessObservation(action.Summary(), insights...)
			r.getLogger().Info("observe tool success",
				"session_id", sessionID,
				"iteration", iter,
				"tools", toolNames,
				"elapsed_ms", time.Since(observeStart).Milliseconds(),
				"insights", len(insights),
			)
		}

	case DecisionAnswer:
		obs = NewSuccessObservation(action.Summary(), "direct answer generated")
		r.getLogger().Info("observe answer",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	case DecisionClarify:
		obs = NewSuccessObservation(action.Summary(), "clarification question generated")
		r.getLogger().Info("observe clarify",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	case DecisionDelegate:
		obs = NewSuccessObservation(action.Summary(), "delegation executed")
		r.getLogger().Info("observe delegate",
			"session_id", sessionID,
			"iteration", iter,
			"elapsed_ms", time.Since(observeStart).Milliseconds(),
		)

	}

	ctx.LastObservation = obs
	return nil
}

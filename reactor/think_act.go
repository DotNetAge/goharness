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
// applies micro-compaction to remove old read-only tool results,
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

	// Apply micro-compaction: remove old read-only tool results
	// while preserving the most recent 2 assistant-turns' worth.
	compactHistory := core.MicroCompact(ctx.ConversationHistory, 2)

	return &CallInput{
		SessionID:            sessionID,
		SystemPromptSections: sections,
		UserMessage:          ctx.Input,
		History:              compactHistory,
		Tools:                llmTools,
	}
}

// Think executes a single thinking phase with full-schema tools.
// The LLM is called with native function calling — no custom JSON output required.
// The Thought struct is derived directly from the native response:
//   - If ToolCalls are present → DecisionAct (execute tools)
//   - If no ToolCalls → DecisionAnswer (LLM answered directly)
//
// Streaming content is accumulated verbatim into Thought.Content.
// Native ToolCalls, ToolCallIDs, and ToolCallList are preserved from the LLM response.
//
// Returns (TokenUsage, error).
// On success, ctx.LastThought is populated with the derived Thought.
// On abort (hook), ctx.TerminationReason is set and error is nil.
// On failure, error is non-nil and ctx.LastThought may be partially set.
func (r *Reactor) Think(ctx *ReactContext) (core.TokenUsage, error) {
	thinkStart := time.Now()
	sessionID := r.resolveSessionID(ctx)
	iter := ctx.CurrentIteration + 1

	r.getLogger().Info("[think] start",
		"session_id", sessionID,
		"iteration", iter,
	)

	callInput := r.buildCallInput(ctx)

	// 防御性检查：确保 LLMCaller 已初始化
	if r.llmCaller == nil {
		r.getLogger().Error("llm caller is nil, cannot execute think",
			fmt.Errorf("LLMCaller not initialized in Reactor"),
			"session_id", sessionID,
			"iteration", iter,
		)
		return core.TokenUsage{}, fmt.Errorf("llm caller not initialized")
	}

	// Thought Before hooks (PreCheck, ThoughtEvent, ThoughtLogger)
	if hr := r.execThoughtHooksBefore(ctx, callInput); hr.IsTerminal() {
		if hr.Error != nil {
			return core.TokenUsage{}, hr.Error
		}
		ctx.TerminationReason = hr.AbortReason
		ctx.LastThought = &Thought{
			Decision:  DecisionAnswer,
			Content:   hr.AbortReason,
			Timestamp: time.Now(),
		}
		return core.TokenUsage{}, nil
	}

	var contentBuf strings.Builder
	var toolCalls []gochatcore.ToolCall

	r.getLogger().Info("[think] llm call start",
		"session_id", sessionID,
		"iteration", iter,
	)

	result := r.llmCaller.CallStreamWithToolDelta(ctx.Ctx(), *callInput,
		func(chunk string) {
			contentBuf.WriteString(chunk)
			ctx.EmitEvent(core.ContentDelta, chunk)
		},
		[]StreamThinkingCallback{
			func(thinkingChunk string) {
				ctx.EmitEvent(core.ThinkingDelta, thinkingChunk)
			},
		},
		func(delta gochatcore.ToolCallDelta) {
			ctx.EmitEvent(core.ToolUseDelta, core.ToolUseDeltaData{
				Index:     delta.Index,
				ID:        delta.ID,
				Name:      delta.Name,
				Arguments: delta.Arguments,
			})
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
		return result.TokenUsage, fmt.Errorf("llm call failed: %w", result.Error)
	}

	content := contentBuf.String()
	if content == "" {
		content = result.Content
	}
	if len(result.ToolCalls) > 0 {
		toolCalls = result.ToolCalls
	}

	// Build Thought directly from native response — no JSON parsing.
	thought := buildThoughtFromNative(content, toolCalls, result.FinishReason, time.Now())

	// Set LastThought before After hooks so hooks can reference it
	ctx.LastThought = thought

	// Thought After hooks (ThoughtEvent, ThoughtLogger, Convergence)
	if hr := r.execThoughtHooksAfter(ctx, thought); hr.IsTerminal() {
		if hr.Error != nil {
			return result.TokenUsage, hr.Error
		}
		ctx.TerminationReason = hr.AbortReason
	}

	r.getLogger().Info("[think] done",
		"session_id", sessionID,
		"iteration", iter,
		"decision", thought.Decision,
		"tool_count", len(thought.ToolCalls),
		"elapsed_ms", time.Since(thinkStart).Milliseconds(),
		"input_tokens", int(result.TokenUsage.InputTokens),
		"output_tokens", int(result.TokenUsage.OutputTokens),
	)

	return result.TokenUsage, nil
}

// buildThoughtFromNative constructs a Thought from the native LLM response.
// No JSON parsing — the Thought is derived from raw content and native tool calls.
func buildThoughtFromNative(content string, toolCalls []gochatcore.ToolCall, finishReason string, ts time.Time) *Thought {
	thought := &Thought{
		Content:      content,
		FinishReason: finishReason,
		Timestamp:    ts,
	}

	if len(toolCalls) > 0 {
		thought.Decision = DecisionAct
		thought.ToolCalls = make(map[string]map[string]any, len(toolCalls))
		thought.ToolCallIDs = make(map[string]string, len(toolCalls))
		thought.ToolCallList = make([]ToolCallItem, 0, len(toolCalls))

		for _, tc := range toolCalls {
			var params map[string]any
			if tc.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
					params = map[string]any{"raw_args": tc.Arguments}
				}
			}
			// ToolCalls map: last writer wins for duplicate names (backward compat)
			thought.ToolCalls[tc.Name] = params
			thought.ToolCallIDs[tc.Name] = tc.ID
			// ToolCallList: every entry preserved in original order
			thought.ToolCallList = append(thought.ToolCallList, ToolCallItem{
				Name:      tc.Name,
				Arguments: params,
				ID:        tc.ID,
			})
		}
	} else {
		thought.Decision = DecisionAnswer
	}

	return thought
}

// Act executes the decision from the Think phase.
// Based on whether the LLM called tools or answered directly:
//   - DecisionAct: Execute tool calls
//   - DecisionAnswer: Use raw Content as the answer
//
// On success, ctx.LastAction is populated with the execution results.
// Returns error if there is no Thought or execution fails.
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
		)
		answer := thought.Content
		if answer == "" {
			answer = thought.Reasoning
		}
		ctx.LastAction = &Action{
			Results: []ToolResult{{ToolName: "answer", Result: answer, Success: true}},
		}
		return nil

	case DecisionAct:
		r.getLogger().Info("act toolcalls",
			"session_id", sessionID,
			"iteration", iter,
			"tool_count", len(thought.ToolCalls),
		)
		return r.executeToolCalls(ctx, thought, start)

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

// parseToolCalls converts Thought.ToolCallList (preferred) or ToolCalls map
// into a slice of toolCall structs for execution. ToolCallList preserves
// ordering and supports duplicate tool names in parallel calls.
func (r *Reactor) parseToolCalls(thought *Thought) []toolCall {
	// ToolCallList is preferred: ordered, supports same-name parallel calls
	if len(thought.ToolCallList) > 0 {
		calls := make([]toolCall, 0, len(thought.ToolCallList))
		for _, item := range thought.ToolCallList {
			calls = append(calls, toolCall{
				name:       item.Name,
				params:     item.Arguments,
				toolCallID: item.ID,
			})
		}
		return calls
	}

	// Fallback to ToolCalls map for backward compatibility (e.g. deserialized
	// JSON from old persistence that only has the map field).
	// NOTE: map key deduplicates same-named tools — only the last survives.
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

// handleEmptyCalls handles the case where Thought.Decision is DecisionAct
// but ToolCalls is empty. Falls back to DecisionAnswer with the raw Content.
func (r *Reactor) handleEmptyCalls(ctx *ReactContext, thought *Thought) error {
	answer := thought.Content
	if answer == "" {
		answer = "Sorry, I cannot determine which tool to use for your request."
	}
	ctx.LastAction = &Action{
		Results: []ToolResult{{ToolName: "answer", Result: answer, Success: true}},
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
		toolCtx, cancel := ctx.withToolTimeout(r.syncToolTimeout)
		tr := r.execToolHooksWithAbort(toolCtx, c)
		cancel()
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
			defer func() {
				if r := recover(); r != nil {
					asyncCh <- asyncResult{
						name:       toolName,
						result:     "",
						err:        fmt.Errorf("async tool %q outer goroutine panicked: %v", toolName, r),
						toolCallID: toolCallID,
					}
				}
			}()

			asyncCtx, cancel := context.WithTimeout(ctx.Ctx(), r.asyncToolTimeout)
			defer cancel()

			ctx.EmitEvent(core.ToolExecStart, core.ToolExecStartData{
				ToolName: toolName,
				Params:   params,
			})

			type innerExecResult struct {
				result *core.ToolExecutionResult
				err    error
			}
			resultCh := make(chan innerExecResult, 1)
			go func() {
				var er innerExecResult
				defer func() {
					if r := recover(); r != nil {
						er.err = fmt.Errorf("tool %q panicked: %v", toolName, r)
					}
					resultCh <- er
				}()
				er.result, er.err = r.toolExecutor.Execute(asyncCtx, toolName, params)
			}()

			var execResult *core.ToolExecutionResult
			var execErr error
			select {
			case er := <-resultCh:
				execResult = er.result
				execErr = er.err
			case <-asyncCtx.Done():
				execResult = nil
				execErr = fmt.Errorf("async tool %q timed out after %v", toolName, r.asyncToolTimeout)
			}

			resultStr := ""
			if execErr == nil && execResult != nil {
				resultStr = execResult.Result
			}

			var metadata any
			if execResult != nil {
				metadata = execResult.Metadata
			}
			tr := ToolResult{
				ToolName:   toolName,
				ToolCallID: toolCallID,
				Duration:   time.Since(start),
				Success:    execErr == nil,
				Result:     resultStr,
				Metadata:   metadata,
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
	ctx.LastAction = &Action{
		Timestamp: start,
		Results:   results,
	}
	return nil
}

package reactor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

var _ = fmt.Sprintf

func mustMarshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// ── executeTurn ─────────────────────────────────────────────────────────────

// executeTurn 执行一轮 TAs：
//
//	① BeforeLLM Hooks
//	② Stream LLM → 收集 content / thinking / toolCalls
//	③ AfterLLM Hooks
//	④ 如果有工具调用 → 执行工具（sync 串行 + async 并行）
//	⑤ 返回 LLMResponse + 工具结果
//
// 所有状态通过参数传入、结果返回，不修改外部状态。
func (r *Reactor) executeTurn(
	ctx context.Context,
	sessionID string,
	input string,
	iteration int,
	history []core.Message,
	callInput *CallInput,
	emit func(core.ReactEventType, any),
) (resp *LLMResponse, results []ToolResult, usage core.TokenUsage, err error) {

	// ── ① BeforeLLM Hooks ──
	for _, h := range r.loopHooks {
		hr := h.BeforeLLM(sessionID, iteration, callInput)
		if hr.IsTerminal() {
			if hr.Error != nil {
				return nil, nil, usage, hr.Error
			}
			return &LLMResponse{
				Content:     hr.AbortReason,
				FinishReason: "abort",
				AbortReason: hr.AbortReason,
			}, nil, usage, nil
		}
	}

	// ── ② Stream LLM ──
	var contentBuf, reasoningBuf strings.Builder

	result := r.llmCaller.CallStreamWithToolDelta(ctx, *callInput,
		func(chunk string) {
			contentBuf.WriteString(chunk)
			emit(core.ContentDelta, chunk)
		},
		[]StreamThinkingCallback{
			func(chunk string) {
				reasoningBuf.WriteString(chunk)
				emit(core.ThinkingDelta, chunk)
			},
		},
		func(delta gochatcore.ToolCallDelta) {
			emit(core.ToolUseDelta, core.ToolUseDeltaData{
				Index:     delta.Index,
				ID:        delta.ID,
				Name:      delta.Name,
				Arguments: delta.Arguments,
			})
		},
	)
	if result.Error != nil {
		emit(core.LLMTimeout, core.LLMTimeoutData{
			SessionID: sessionID,
			Timeout:   r.llmCaller.streamTimeout,
			Elapsed:   time.Since(time.Now()),
			Error:     result.Error.Error(),
		})
		return nil, nil, result.TokenUsage, result.Error
	}

	content := contentBuf.String()

	// 构建 LLMResponse
	resp = &LLMResponse{
		Content:      content,
		Reasoning:    reasoningBuf.String(),
		FinishReason: result.FinishReason,
		ToolCalls:    parseToolInvocations(result.ToolCalls),
		TokenUsage:   result.TokenUsage,
	}

	emit(core.ThinkingDone, resp)

	// ── ③ AfterLLM Hooks ──
	for _, h := range r.loopHooks {
		hr := h.AfterLLM(sessionID, iteration, resp, results)
		if hr.IsTerminal() {
			if hr.Error != nil {
				return resp, results, result.TokenUsage, hr.Error
			}
			resp.AbortReason = hr.AbortReason
			return resp, results, result.TokenUsage, nil
		}
	}

	// ── ④ 没有工具 → 直接回答 ──
	if len(resp.ToolCalls) == 0 {
		return resp, []ToolResult{{
			ToolName: "answer",
			Result:   resp.Content,
			Success:  true,
		}}, result.TokenUsage, nil
	}

	// ── ⑤ 执行工具 ──
	results = r.executeTools(ctx, sessionID, resp.ToolCalls, emit)
	return resp, results, result.TokenUsage, nil
}

// ── parseToolInvocations ───────────────────────────────────────────────────

func parseToolInvocations(calls []gochatcore.ToolCall) []ToolCallInvocation {
	if len(calls) == 0 {
		return nil
	}
	invocs := make([]ToolCallInvocation, len(calls))
	for i, tc := range calls {
		var params map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
				params = map[string]any{"raw_args": tc.Arguments}
			}
		}
		invocs[i] = ToolCallInvocation{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: params,
		}
	}
	return invocs
}

// ── executeTools ───────────────────────────────────────────────────────────

// executeTools 执行一批工具调用。
// Sync 工具串行执行，Async 工具并行执行。
func (r *Reactor) executeTools(
	ctx context.Context,
	sessionID string,
	invocs []ToolCallInvocation,
	emit func(core.ReactEventType, any),
) []ToolResult {
	if len(invocs) == 0 {
		return nil
	}

	results := make([]ToolResult, len(invocs))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, inv := range invocs {
		isAsync := r.isToolAsync(inv.Name)

		if isAsync {
			wg.Add(1)
			i, inv := i, inv
			go func() {
				defer wg.Done()
				tr := r.executeSingleTool(ctx, sessionID, inv, emit, r.asyncToolTimeout)
				mu.Lock()
				results[i] = tr
				mu.Unlock()
			}()
		} else {
			tr := r.executeSingleTool(ctx, sessionID, inv, emit, r.syncToolTimeout)
			results[i] = tr
		}
	}

	wg.Wait()
	return results
}

// isToolAsync reports whether the tool is configured as async.
func (r *Reactor) isToolAsync(name string) bool {
	if tool, ok := r.toolRegistry.Get(name); ok {
		return tool.Info().IsAsync
	}
	return false
}

// executeSingleTool 执行单个工具调用，经过完整的 ToolHook 链。
func (r *Reactor) executeSingleTool(
	ctx context.Context,
	sessionID string,
	inv ToolCallInvocation,
	emit func(core.ReactEventType, any),
	timeout time.Duration,
) ToolResult {
	start := time.Now()

	// ToolHook.Before (runs before ToolExecStart, so denied tools don't emit)
	for _, h := range r.toolHooks {
		hr := h.Before(sessionID, inv.Name, inv.Arguments)
		if hr.Abort {
			emit(core.PermissionDenied, hr.AbortReason)
			return r.failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
		}
		if hr.Error != nil {
			emit(core.PermissionDenied, hr.Error.Error())
			return r.failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
		}
	}

	emit(core.ToolExecStart, core.ToolExecStartData{
		ToolName: inv.Name,
		Params:   inv.Arguments,
	})

	// 执行
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execResult, execErr := r.toolExecutor.Execute(execCtx, inv.Name, inv.Arguments)
	tr := r.buildToolResult(inv, execResult, execErr, start)

	// ToolHook.After
	for _, h := range r.toolHooks {
		hr := h.After(&tr)
		if hr.Abort {
			tr = r.failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
			break
		}
		if hr.Error != nil {
			tr = r.failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
			break
		}
	}

	emit(core.ToolExecEnd, core.ToolExecEndData{
		ToolName:   tr.ToolName,
		ToolCallID: tr.ToolCallID,
		Duration:   time.Since(start),
		Success:    tr.Success,
		Result:     tr.Result,
		Error:      tr.Error,
	})

	return tr
}

func (r *Reactor) failedToolResult(toolName, toolCallID, errMsg string, start time.Time) ToolResult {
	return ToolResult{
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Success:    false,
		Error:      errMsg,
		Duration:   time.Since(start),
	}
}

func (r *Reactor) buildToolResult(inv ToolCallInvocation, execResult *core.ToolExecutionResult, execErr error, start time.Time) ToolResult {
	tr := ToolResult{
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

// convertParametersToSchema converts []core.Parameter to a JSON schema map.
func convertParametersToSchema(params []core.Parameter) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
	if len(params) == 0 {
		return schema
	}
	props := schema["properties"].(map[string]any)
	req := schema["required"].([]string)
	for _, p := range params {
		prop := map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		props[p.Name] = prop
		if p.Required {
			req = append(req, p.Name)
		}
	}
	if len(req) == 0 {
		delete(schema, "required")
	} else {
		schema["required"] = req
	}
	return schema
}

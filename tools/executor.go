package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/google/uuid"
)

// NewToolExecutor 创建一个新的工具执行器实例。
//
// 参数：
//   - registry: 工具注册表，用于查找和执行工具
//   - opts: 可选的执行器配置选项（如权限检查器、日志、事件发射器等）
//
// 返回：
//   - ToolExecutor: 配置好的执行器实例
//
// 示例：
//
//	executor := NewToolExecutor(registry,
//	    WithPermissionChecker(checker),
//	    WithLogger(logger),
//	)
//	result, err := executor.Execute(ctx, "Bash", params)
func NewToolExecutor(registry ToolRegistry, opts ...ExecutorOption) ToolExecutor {
	cfg := &executorConfig{registry: registry}
	for _, opt := range opts {
		opt(cfg)
	}
	return &implToolExecutor{cfg: cfg}
}

// implToolExecutor 是 ToolExecutor 接口的默认实现。
// 它提供了完整的工具执行流程，包括：
//   - 工具查找和验证
//   - 权限检查（支持自动允许/拒绝/询问用户）
//   - 上下文注入（ToolContext）
//   - 执行结果处理（序列化、截断、错误增强）
type implToolExecutor struct {
	cfg *executorConfig
}

// ResetCycle 重置执行器的周期状态。
// 当前为空实现，保留用于未来扩展（如速率限制、配额管理）。
func (e *implToolExecutor) ResetCycle() {}

// Execute 执行指定名称的工具。
//
// 执行流程：
//  1. 从注册表中查找工具
//  2. 构建工具使用上下文 (ToolUseContext)
//  3. 权限检查（如果配置了权限检查器）
//  4. 如果需要用户授权，等待用户响应
//  5. 构建并注入 ToolContext
//  6. 调用工具的 Execute 方法
//  7. 处理执行结果（序列化、截断）
//  8. 返回 ToolExecutionResult
//
// 参数：
//   - ctx: 上下文，支持取消和超时
//   - name: 要执行的工具名称
//   - params: 工具参数映射
//
// 返回：
//   - *ToolExecutionResult: 执行结果（包含结果内容、持续时间等）
//   - error: 仅在严重错误时返回（如工具不存在），工具执行错误在 Result.Error 中
func (e *implToolExecutor) Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error) {
	tool, ok := e.cfg.registry.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}

	toolInfo := tool.Info()
	useCtx := &ToolUseContext{
		ToolName: name,
		ToolInfo: toolInfo,
		Params:   params,
		Ctx:      ctx,
	}

	if e.cfg.permissionChecker != nil {
		permResult := e.cfg.permissionChecker.CheckPermissions(useCtx)
		if e.cfg.logger != nil {
			e.cfg.logger.Debug("[executor] permission check", "tool", name, "behavior", permResult.Behavior, "msg", permResult.Message)
		}
		switch permResult.Behavior {
		case PermissionDeny:
			if e.cfg.eventEmitter != nil {
				e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionDenied, permResult.Message))
			}
			if e.cfg.logger != nil {
				e.cfg.logger.Info("[executor] tool denied", "tool", name, "reason", permResult.Message)
			}
			return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied: %s", name, permResult.Message)}, nil

		case PermissionAsk:
			if e.cfg.logger != nil {
				e.cfg.logger.Info("[executor] awaiting user response", "tool", name)
			}
			result := e.awaitUserResponse(name, params, toolInfo.SecurityLevel, useCtx)
			switch result.Behavior {
			case PermissionDeny:
				if e.cfg.eventEmitter != nil {
					e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionDenied, result.Message))
				}
				return &ToolExecutionResult{ToolName: name, Error: fmt.Errorf("tool %q denied by user: %s", name, result.Message)}, nil
			case PermissionAllow:
				if result.UpdatedInput != nil {
					params = result.UpdatedInput
					useCtx.Params = params
				}
			}
		case PermissionAllow:
			if permResult.UpdatedInput != nil {
				params = permResult.UpdatedInput
				useCtx.Params = params
			}
		}
	}

	if e.cfg.logger != nil {
		e.cfg.logger.Debug("[executor] executing tool", "tool", name, "params", params)
	}

	toolCtx := &ToolContext{
		EmitEvent:   e.cfg.eventEmitter,
		ResultStore: e.cfg.resultStore,
		KVStore:     e.cfg.kvStore,
		FileStore:   e.cfg.fileStore,
		SessionID:   e.cfg.sessionID,
		Logger:      e.cfg.logger,
		ProjectDir:  e.cfg.projectDir,
		SessionDir:  e.cfg.sessionDir,
	}
	execCtx := WithToolContext(ctx, toolCtx)

	start := time.Now()
	result, err := tool.Execute(execCtx, params)
	duration := time.Since(start)

	if e.cfg.logger != nil {
		if err != nil {
			e.cfg.logger.Debug("[executor] tool execution error", "tool", name, "duration_ms", duration.Milliseconds(), "error", err.Error())
		} else {
			e.cfg.logger.Debug("[executor] tool execution completed", "tool", name, "duration_ms", duration.Milliseconds())
		}
	}

	if err != nil {
		enhanced := enhanceFileError(err, extractPath(params))
		return &ToolExecutionResult{ToolName: name, Duration: duration, Error: enhanced}, nil
	}

	str, ok := result.(string)
	if !ok {
		b, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			str = fmt.Sprintf("%v", result)
		} else {
			str = string(b)
		}
	}

	str = e.processResult(name, str, toolInfo)

	return &ToolExecutionResult{
		Result:   str,
		Duration: duration,
		ToolName: name,
	}, nil
}

// awaitUserResponse 等待用户对工具执行的授权响应。
//
// 对于 AskUser 工具，会发送 AskUserRequest 事件并等待用户回答问题。
// 对于其他工具，会发送 PermissionRequest 事件并等待用户批准或拒绝。
//
// 参数：
//   - name: 工具名称
//   - params: 工具参数（用于提取 AskUser 的问题）
//   - secLevel: 工具的安全级别
//   - useCtx: 工具使用上下文（包含会话 ID、任务 ID 等）
//
// 返回：
//   - PermissionResult: 用户授权结果（允许/拒绝，可能包含修改后的输入）
func (e *implToolExecutor) awaitUserResponse(name string, params map[string]any, secLevel events.SecurityLevel, useCtx *ToolUseContext) PermissionResult {
	ch := make(chan PermissionResult, 1)

	if name == "AskUser" {
		questions := extractAskUserQuestions(params)
		if e.cfg.eventEmitter != nil {
			e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.AskUserRequest,
				events.NewAskUserRequestData(uuid.New().String(), questions, func(answers map[string]string) {
					ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: map[string]any{"answers": answers}}
				}),
			))
		}
		select {
		case result := <-ch:
			return result
		case <-useCtx.Ctx.Done():
			return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
		}
	}

	if e.cfg.eventEmitter != nil {
		e.cfg.eventEmitter(events.NewReactEvent(useCtx.SessionID, useCtx.TaskID, "", events.PermissionRequest,
			events.NewPermissionRequestData(
				uuid.New().String(),
				name,
				params,
				"This tool requires your approval before execution.",
				secLevel,
				func(updatedInput map[string]any) {
					ch <- PermissionResult{Behavior: PermissionAllow, UpdatedInput: updatedInput}
				},
				func(reason string) {
					ch <- PermissionResult{Behavior: PermissionDeny, Message: reason}
				},
			),
		))
	}
	select {
	case result := <-ch:
		return result
	case <-useCtx.Ctx.Done():
		return PermissionResult{Behavior: PermissionDeny, Message: "context cancelled"}
	}
}

// extractAskUserQuestions 从 AskUser 工具参数中提取问题信息。
// 将 params 映射转换为 events.AskUserQuestion 结构体列表。
func extractAskUserQuestions(params map[string]any) []events.AskUserQuestion {
	question, _ := params["question"].(string)
	if question == "" {
		return nil
	}
	multi, _ := params["multiSelect"].(bool)
	q := events.AskUserQuestion{
		Question:    question,
		MultiSelect: multi,
	}
	if opts, ok := params["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				q.Options = append(q.Options, s)
			}
		}
	}
	return []events.AskUserQuestion{q}
}

// enhanceFileError 增强文件相关错误信息。
// 当错误是 os.ErrNotExist（文件不存在）时，会在错误消息中添加相似路径建议，
// 帮助用户快速定位可能的正确路径。
//
// 参数：
//   - err: 原始错误
//   - path: 尝试访问的文件路径
//
// 返回：
//   - error: 增强后的错误信息（可能包含路径建议）
func enhanceFileError(err error, path string) error {
	if path == "" || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(path)
	suggestions := findSimilarPaths(dir, filepath.Base(path))
	if len(suggestions) == 0 {
		return fmt.Errorf("%w\nFile not found: %s", err, path)
	}
	return fmt.Errorf("%w\nFile not found: %s\n\nDid you mean one of these?\n%s",
		err, path, strings.Join(suggestions, "\n"))
}

// extractPath 从参数映射中提取文件路径。
// 按优先级检查常见的路径参数名：path, file_path, filepath, dir, directory。
//
// 参数：
//   - params: 工具参数映射
//
// 返回：
//   - string: 找到的第一个有效路径，如果都没有则返回空字符串
func extractPath(params map[string]any) string {
	for _, key := range []string{"path", "file_path", "filepath", "dir", "directory"} {
		if v, ok := params[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// findSimilarPaths 在指定目录中查找与目标文件名相似的文件。
// 用于在文件不存在时提供路径建议。
//
// 匹配规则：
//   - 文件名包含目标基础名称（不区分大小写）
//   - 或目标基础名称包含文件名（不区分大小写）
//
// 最多返回 3 个匹配结果。
//
// 参数：
//   - dir: 要搜索的目录
//   - base: 目标文件的基础名称
//
// 返回：
//   - []string: 相似文件的完整路径列表
func findSimilarPaths(dir, base string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var suggestions []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(strings.ToLower(name), strings.ToLower(base)) ||
			strings.Contains(strings.ToLower(base), strings.ToLower(name)) {
			suggestions = append(suggestions, filepath.Join(dir, name))
			if len(suggestions) >= 3 {
				break
			}
		}
	}
	return suggestions
}

// processResult 处理工具执行结果，根据工具配置进行截断。
//
// 截断规则：
//   - 如果 MaxResultSizeChars 为 -1，不截断
//   - 如果 MaxResultSizeChars > 0，使用该值作为阈值
//   - 否则使用默认阈值 25000 字符
//
// 超过阈值的结果会被截断，并附加截断提示信息。
//
// 参数：
//   - toolName: 工具名称（用于日志）
//   - str: 原始结果字符串
//   - toolInfo: 工具元信息（包含大小限制配置）
//
// 返回：
//   - string: 处理后的结果字符串（可能被截断）
func (e *implToolExecutor) processResult(toolName, str string, toolInfo *ToolInfo) string {
	if toolInfo.MaxResultSizeChars == -1 {
		return str
	}
	threshold := 25000
	if toolInfo.MaxResultSizeChars > 0 {
		threshold = toolInfo.MaxResultSizeChars
	}
	runes := []rune(str)
	if len(runes) <= threshold {
		return str
	}
	return string(runes[:threshold]) + "\n... [truncated: result exceeds size limit] ..."
}

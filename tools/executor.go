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
)

// NewToolExecutor 创建一个新的工具执行器实例。
//
// 参数：
//   - registry: 工具注册表，用于查找和执行工具
//   - opts: 可选的执行器配置选项（如日志、事件发射器等）
//
// 返回：
//   - ToolExecutor: 配置好的执行器实例
//
// 示例：
//
//	executor := NewToolExecutor(registry, WithLogger(logger))
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
//  2. 构建并注入 ToolContext（带 session、logger、event emitter 等）
//  3. 调用工具的 Execute 方法
//  4. 处理执行结果（序列化、截断）
//  5. 返回 ToolExecutionResult
//
// 权限检查在调用此方法之前由 Runtime 完成（见 tools.PermissionRequired）。
// Executor 不再承担权限决策 — 这是单一职责分工：Runtime 负责"是否允许"，
// 工具的 Grant() 负责"为什么需要批准"，Executor 只关心拿到 args 后如何
// 把工具跑起来。
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

	if e.cfg.logger != nil {
		e.cfg.logger.Debug("[executor] executing tool", "tool", name, "params", params)
	}

	toolCtx := &ToolContext{
		EmitEvent:   e.cfg.eventEmitter,
		ResultStore: e.cfg.resultStore,
		KVStore:     e.cfg.kvStore,
		FileStore:   e.cfg.fileStore,
		Logger:      e.cfg.logger,
		Session:     e.cfg.session,
	}
	execCtx := WithToolContext(ctx, toolCtx)

	start := time.Now()

	// Protect against panics in tool execution that could crash the process.
	var (
		result   any
		err      error
		panicVal any
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicVal = r
			}
		}()
		result, err = tool.Execute(execCtx, params)
	}()
	if panicVal != nil {
		err = fmt.Errorf("tool %q panicked: %v", name, panicVal)
	}
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

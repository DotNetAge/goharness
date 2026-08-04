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
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("调用工具 %q，但该工具未注册", name),
			"工具名称不在已注册工具列表中（可能拼写错误或未在当前会话启用）",
			"先自查：我传入的工具名称是否拼写正确、是否为当前可用的工具？可对照已注册工具列表核对后重新调用",
		))
	}

	toolInfo := tool.Info()

	if e.cfg.logger != nil {
		e.cfg.logger.Debug("[executor] executing tool", "tool", name, "params", params)
	}

	toolCtx := &ToolContext{
		EmitEvent:    e.cfg.eventEmitter,
		SessionStore: e.cfg.sessionStore,
		KVStore:      e.cfg.kvStore,
		FileStore:    e.cfg.fileStore,
		Logger:       e.cfg.logger,
		Session:      e.cfg.session,
	}
	execCtx := WithToolContext(ctx, toolCtx)

	start := time.Now()

	// 防止工具执行中的 panic 导致进程崩溃。
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
		err = fmt.Errorf("工具 %q 发生 panic: %v", name, panicVal)
	}
	duration := time.Since(start)

	if e.cfg.logger != nil {
		if err != nil {
			e.cfg.logger.Debug("[executor] 工具执行错误", "tool", name, "duration_ms", duration.Milliseconds(), "error", err.Error())
		} else {
			e.cfg.logger.Debug("[executor] 工具执行完成", "tool", name, "duration_ms", duration.Milliseconds())
		}
	}

	if err != nil {
		enhanced := enhanceError(err, name, extractPath(params))
		return &ToolExecutionResult{ToolName: name, Duration: duration, Error: enhanced}, nil
	}

	// 提取工具返回的图片数据（如 Read 读取的图片文件）。
	// 必须在 JSON 序列化之前完成：ReadResult.Images 带 json:"-" 标签，
	// 一旦走 json.Marshal 图片就会被静默丢弃，导致 Hook 层拿不到图片。
	ter := &ToolExecutionResult{
		Duration: duration,
		ToolName: name,
	}
	if rr, isRead := result.(*ReadResult); isRead {
		ter.Images = rr.Images
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

	ter.Result = str
	return ter, nil
}

// enhanceError 对工具执行错误做引导增强。
//
// 处理原则（可预知 → 精确引导；不可预知 → 零包装兜底）：
//  1. 错误已是引导格式（含「下一步我应该」标记）→ 原样返回，不做二次包装
//  2. 文件不存在（os.ErrNotExist）→ 复用 enhanceFileError 的相似路径建议
//  3. 其它（未知）错误 → 直接返回原始错误，不给任何指引。
//     兜底场景无法预知解决方案，添加「请重试」式话术只会诱导（尤其是
//     小模型）陷入反复重试的死循环，因此原样返回是更安全的选择。
//
// 参数：
//   - err: 原始错误
//   - toolName: 工具名称（保留参数，供未来需要时使用）
//   - path: 尝试访问的文件路径（可能为空）
//
// 返回：
//   - error: 原始错误或引导格式的错误信息
func enhanceError(err error, toolName, path string) error {
	if err == nil {
		return nil
	}
	// 已是引导格式的错误直接透传，避免二次包装破坏精确引导。
	if strings.Contains(err.Error(), "下一步我应该") {
		return err
	}
	if path != "" && errors.Is(err, os.ErrNotExist) {
		return enhanceFileError(err, path)
	}
	// 超出预知范围的兜底：直接返回原始错误，不做任何包装、不给指引。
	// 原因：未知错误无法预知解决方案，任何「请重试」式话术都会诱导
	// （尤其是小模型）陷入反复重试的死循环；工具名已由执行结果携带，
	// 因此原样返回是更安全的选择。
	return err
}

// enhanceFileError 增强文件相关错误信息。
// 当错误是 os.ErrNotExist（文件不存在）时，将错误改写为第一人称引导格式，
// 并在存在相似路径时附加候选建议，帮助模型快速定位可能的正确路径。
//
// 参数：
//   - err: 原始错误
//   - path: 尝试访问的文件路径
//
// 返回：
//   - error: 引导格式的错误信息（可能包含相似路径建议）
func enhanceFileError(err error, path string) error {
	if path == "" || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// 复用 GuideReadFileNotFound 的引导文案（单一文案来源，避免双套内容漂移）。
	dir := filepath.Dir(path)
	suggestions := findSimilarPaths(dir, filepath.Base(path))
	guide := GuideReadFileNotFound(path)
	if len(suggestions) == 0 {
		return fmt.Errorf("%s（原始错误：%w）", guide, err)
	}
	return fmt.Errorf("%s（原始错误：%w）\n\n您是不是要找以下其中一个?\n%s",
		guide, err, strings.Join(suggestions, "\n"))
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
	return string(runes[:threshold]) + "\n... [已截断: 结果超出大小限制] ..."
}

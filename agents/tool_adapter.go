package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DotNetAge/goagent"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/tools"
)

// toolContextValue 是 goharness 侧声明的 RuntimeValue 槽位，承载工具运行依赖
// *tools.ToolContext（Session/KVStore/SessionStore/Logger 等的权威载体）。
//
// 注入时机：AskBuilder.Run() 换核装配时经 goagent.WithRuntimeValue 写入 Agent；
// 取回时机：funcToolAdapter.Execute 执行前从 ctx 取回并以 WithToolContext 注入，
// 现有 15+ 内置工具与 MCP 工具经 tools.GetToolContext(ctx) 的读取路径零改动。
var toolContextValue = goagent.NewRuntimeValue[*tools.ToolContext]("goharness.tool_context")

// PermissionRequestDetails 是权限挂起请求（kind="permission"）的 Details 载体，
// 由 funcToolAdapter 在 Grant 未通过时填充，事件桥接器据它合成 PermissionPending 事件。
type PermissionRequestDetails struct {
	// Reason 是 Grant 返回的人类可读说明（UI 弹窗展示）。
	Reason string
	// Params 是工具的原始参数（UI 展示待授权的操作详情）。
	Params map[string]any
}

// AskUserRequestDetails 是用户提问挂起请求（kind="ask_user"）的 Details 载体，
// 由 funcToolAdapter 在检测到 AskUser 调用时填充，事件桥接器据它合成 AskUserPending 事件。
type AskUserRequestDetails struct {
	// Question 是向用户提出的澄清性问题。
	Question string
	// Options 是可选的答案选项列表（开放式问题为空）。
	Options []string
	// MultiSelect 表示是否允许用户选择多个选项。
	MultiSelect bool
}

// funcToolAdapter 将 goharness 的 tools.FuncTool 适配为 goagent 的 ToolCall：
//   - 元信息：Info() 的 Name/Description 直通；Prompt（给 LLM 的详细说明）非空时
//     以空行拼接进 Description；参数定义优先 RawSchema（MCP 完整透传），
//     否则复用 buildParamSchema 扁平转换——与旧核 buildAllToolDefinitions 口径一致；
//   - 执行：args（json.RawMessage）→ map 参数 → Grant 授权检查 → FuncTool.Execute →
//     结果统一序列化为 string。
//
// 阶段一语义说明：
//   - 授权未通过时不执行工具，返回 goagent 挂起错误（NewPermissionRequest），
//     由 goagent 内核发射 EvSuspend 并以 StopSuspended 结束本轮——
//     与旧核「PermissionPending + 循环终止、工具不执行」的产品语义对齐；
//   - AskUser 工具调用转为 goagent 挂起请求（NewAskUserRequest），
//     对齐旧核「执行后检测 findAskUserInvocation → ask_user_pending 终止」；
//   - 旧核的 sync/async 双超时与 IsAsync 并发调度不在适配器复刻：
//     goagent 循环顺序执行工具、超时保护由工具自身与 ctx 取消链路承载。
type funcToolAdapter struct {
	tool tools.FuncTool
}

// newFuncToolAdapter 包装一个 goharness 工具为 goagent ToolCall。
func newFuncToolAdapter(tool tools.FuncTool) goagent.ToolCall {
	return &funcToolAdapter{tool: tool}
}

// Name 返回工具名称（与旧核 LLM 可见名称一致）。
func (a *funcToolAdapter) Name() string {
	return a.tool.Info().Name
}

// Description 返回工具描述；Prompt 非空时合并（旧核 ToolInfo.Prompt 同样面向 LLM）。
func (a *funcToolAdapter) Description() string {
	info := a.tool.Info()
	if info.Prompt == "" {
		return info.Description
	}
	return info.Description + "\n\n" + info.Prompt
}

// Parameters 返回 JSON Schema 形式的参数定义：RawSchema 优先（MCP 工具完整透传），
// 否则由 []Parameter 扁平构建。
func (a *funcToolAdapter) Parameters() json.RawMessage {
	info := a.tool.Info()
	if len(info.RawSchema) > 0 {
		b, err := json.Marshal(info.RawSchema)
		if err == nil {
			return b
		}
		// 序列化失败回退扁平构建（与旧核口径一致）
	}
	return buildParamSchema(info.Parameters)
}

// Execute 执行工具：ToolContext 注入 → 参数解析 → 授权检查 → 执行 → 结果序列化。
// 返回的挂起错误（ErrNeedExternalInput）由 goagent 内核统一处理（EvSuspend + 循环挂起）。
func (a *funcToolAdapter) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. 从 RuntimeValue 取回会话级工具上下文并注入 ctx：
	//    未注入（如独立单测）时保持空 ToolContext 的兜底行为，与旧执行器一致。
	if tc, ok := toolContextValue.From(ctx); ok {
		ctx = tools.WithToolContext(ctx, tc)
	}

	// 2. 参数解析：json.RawMessage → map[string]any（空参数视为空 map）
	params := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("工具 %q 参数解析失败: %w", a.Name(), err)
		}
	}

	// 3. 授权检查：实现 PermissionRequired 的工具（Bash/Write/Edit/RunScript 等）
	//    在执行前做 Grant 检查；未通过即挂起等待用户授权，工具不执行。
	//    ToolCallID 由 goagent 内核在挂起检测处从 call 回填，适配器无需填写。
	if pr, ok := a.tool.(tools.PermissionRequired); ok {
		if granted, reason := pr.Grant(ctx, params); !granted {
			return "", goagent.NewPermissionRequest(ctx, a.Name(), "", args, &PermissionRequestDetails{
				Reason: reason,
				Params: params,
			})
		}
	}

	// 4. 执行工具（AskUser 不走此路径：由 toolExecutorAdapter.Execute 路由为挂起请求）
	result, err := a.tool.Execute(ctx, params)
	if err != nil {
		return "", err
	}

	// 5. 结果序列化：string 直通，其余 json.Marshal，失败时 fmt 兜底
	//    （与旧核 implToolExecutor.Execute 的序列化口径一致）。
	if s, ok := result.(string); ok {
		return s, nil
	}
	b, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return fmt.Sprintf("%v", result), nil
	}
	return string(b), nil
}

// adaptAskUserCall 将 AskUser 工具调用转为 goagent 挂起请求。
// 由换核装配层在 goagent LoopHook / 执行器路由处调用：检测到 LLM 调用名为
// "AskUser" 的工具时，不执行工具，直接返回 ask_user 挂起请求（Details 携带
// 问题与选项，供事件桥接器合成 AskUserPending 事件）。
func adaptAskUserCall(ctx context.Context, args json.RawMessage) (string, error) {
	params := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("工具 AskUser 参数解析失败: %w", err)
		}
	}
	details := &AskUserRequestDetails{}
	if q, ok := params["question"].(string); ok {
		details.Question = q
	}
	if opts, ok := params["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				details.Options = append(details.Options, s)
			}
		}
	}
	if ms, ok := params["multiSelect"].(bool); ok {
		details.MultiSelect = ms
	}
	return "", goagent.NewAskUserRequest(ctx, "AskUser", "", args, details)
}

// toolExecutorAdapter 将 goharness 的 tools.ToolRegistry 整体适配为
// goagent 的 ToolExecutor + ToolEnumerator：
//   - Execute 按 Grant 检查 → ToolHook 链 → goharness 执行器（implToolExecutor：
//     ToolContext 注入/panic 恢复/图片提取/序列化截断全链路复用）的顺序执行；
//   - Tools 暴露全部工具（goagent 内核经 ToolEnumerator 收集 LLM 可见工具定义）；
//   - exclude 按 Agent 名称过滤工具（旧核 ExcludeToolsFor + 子会话多 Agent 工具
//     屏蔽的等价物），被排除的工具既不出现在 LLM 工具清单，执行时也拒绝调用；
//   - 执行前后走 ToolHook 链（Before 可跳过执行返回缓存结果 / 拒绝调用，
//     After 可替换结果），并经 hookCoord 记录结果累积与重复错误引导——
//     对齐旧核 executeSingleTool 的完整 Hook 链语义。
type toolExecutorAdapter struct {
	registry tools.ToolRegistry
	// executor 为 goharness 完整执行器（implToolExecutor），承载 ToolContext
	// 注入、panic 恢复、图片提取与结果序列化——适配层不重复实现。
	executor tools.ToolExecutor
	// exclude 返回需排除的工具名集合（nil 表示全量可用）。
	exclude func() map[string]bool
	// toolHooks 为工具执行钩子链（FileModifyHook/ToolLoggerHook/ImageHook 等）。
	toolHooks []hooks.ToolHook
	// coord 为钩子协调器（结果累积 + 重复错误引导计数），可 nil（无钩子场景）。
	coord *hookCoord
	// emit 用于 Before 链拒绝时发射 PermissionDenied 事件（对齐旧核），可 nil。
	emit func(events.ReactEventType, any)
	// sessionID 为事件元信息（Before 链拒绝事件归属会话）。
	sessionID string
}

// newToolExecutorAdapter 包装工具注册表与 goharness 执行器。
// exclude 在每次 Execute/Tools 时惰性求值，支持子会话与主会话复用同一注册表
// 但持有不同的排除集合。
func newToolExecutorAdapter(
	registry tools.ToolRegistry,
	executor tools.ToolExecutor,
	exclude func() map[string]bool,
	toolHooks []hooks.ToolHook,
	coord *hookCoord,
	emit func(events.ReactEventType, any),
	sessionID string,
) *toolExecutorAdapter {
	return &toolExecutorAdapter{
		registry:  registry,
		executor:  executor,
		exclude:   exclude,
		toolHooks: toolHooks,
		coord:     coord,
		emit:      emit,
		sessionID: sessionID,
	}
}

// excluded 判断工具是否被当前会话排除。
func (a *toolExecutorAdapter) excluded(name string) bool {
	if a.exclude == nil {
		return false
	}
	set := a.exclude()
	return set[name]
}

// Tools 返回全部未排除工具的 goagent ToolCall 视图（实现 goagent.ToolEnumerator）。
func (a *toolExecutorAdapter) Tools() []goagent.ToolCall {
	all := a.registry.All()
	out := make([]goagent.ToolCall, 0, len(all))
	for _, t := range all {
		if a.excluded(t.Info().Name) {
			continue
		}
		out = append(out, newFuncToolAdapter(t))
	}
	return out
}

// Execute 按名称查找并执行工具（实现 goagent.ToolExecutor）。
// 完整流程（对齐旧核 checkPermissionGrants + executeSingleTool）：
// 排除/未注册拒绝 → Grant 授权检查（未通过即挂起）→ Before 钩子链（跳过/拒绝）→
// goharness 执行器执行 → After 钩子链 → 重复错误引导 → hookCoord 结果累积。
func (a *toolExecutorAdapter) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if a.excluded(name) {
		return "", fmt.Errorf("工具 %q 在当前会话中不可用", name)
	}
	tool, ok := a.registry.Get(name)
	if !ok {
		return "", fmt.Errorf("goagent: 未注册工具 %q", name)
	}
	// AskUser 特殊路由：转为挂起请求而非执行（语义见 adaptAskUserCall）。
	if name == "AskUser" {
		return adaptAskUserCall(ctx, args)
	}

	// 参数解析：json.RawMessage → map（Grant/Hook 链与旧核一致消费 map 参数）。
	params := map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("工具 %q 参数解析失败: %w", name, err)
		}
	}

	// ── Grant 授权检查：对齐旧核 runtime 层 checkPermissionGrants 位置 ──
	// 实现 PermissionRequired 的工具未通过 Grant 时挂起等待用户授权，
	// 工具不执行；ToolCallID 由 goagent 内核在挂起检测处回填。
	if pr, ok := tool.(tools.PermissionRequired); ok {
		if granted, reason := pr.Grant(ctx, params); !granted {
			return "", goagent.NewPermissionRequest(ctx, name, "", args, &PermissionRequestDetails{
				Reason: reason,
				Params: params,
			})
		}
	}

	// ── ToolHook.Before 链：可跳过执行返回缓存结果，或拒绝调用 ──
	for _, h := range a.toolHooks {
		hr := h.Before(a.sessionID, name, params)
		if hr.SkipWithResult != nil {
			cached := *hr.SkipWithResult
			if a.coord != nil {
				a.coord.recordToolResult(cached)
			}
			return cached.Result, nil
		}
		if hr.Abort || hr.Error != nil {
			msg := hr.AbortReason
			if hr.Error != nil {
				msg = hr.Error.Error()
			}
			if a.emit != nil {
				a.emit(events.PermissionDenied, msg)
			}
			if a.coord != nil {
				a.coord.recordToolResult(failedToolResult(name, "", msg, time.Now()))
			}
			return "", fmt.Errorf("%s", msg)
		}
	}

	// ── 执行工具（goharness 完整执行器：ToolContext 注入/panic 恢复/
	// 图片提取/序列化截断全链路复用）──
	start := time.Now()
	execResult, execErr := a.executor.Execute(ctx, name, params)
	tr := buildToolResult(hooks.ToolCallInvocation{Name: name, Arguments: params}, execResult, execErr, start)

	// ── ToolHook.After 链：可替换为失败结果 ──
	for _, h := range a.toolHooks {
		hr := h.After(&tr)
		if hr.Abort || hr.Error != nil {
			msg := hr.AbortReason
			if hr.Error != nil {
				msg = hr.Error.Error()
			}
			tr = failedToolResult(name, tr.ToolCallID, msg, start)
			break
		}
	}

	// ── 重复错误引导：连续相同错误达到阈值时在结果末尾追加反思话术 ──
	// 引导必须发生在结果进入 goagent 内核内存消息之前（回写期修改只影响
	// 持久化、不影响下一轮 LLM 请求）。错误经内核统一包装为
	// "工具执行失败: <文本>"，话术随错误文本进入下一轮上下文。
	if a.coord != nil {
		if guide, _ := a.coord.dupTracker.maybeGuide(tr); guide != "" {
			if tr.Error != "" {
				tr.Error = tr.Error + "\n\n" + guide
			} else {
				tr.Result = tr.Result + "\n\n" + guide
			}
		}
		a.coord.recordToolResult(tr)
	}

	if tr.Error != "" {
		return "", fmt.Errorf("%s", tr.Error)
	}
	return tr.Result, nil
}

package agents

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/google/uuid"
)

// permissionSinkKeyType 用于在 context 中传递子会话授权请求的旁路发送器。
// 子会话（b.permissionCh != nil）触发授权时，优先调用该发送器将授权请求
// 直达前端，而不依赖父 exec EventBus 的存活——
// 父 exec 结束/被取消后其 EventBus 订阅已销毁，原 parentEmit 转发链路会静默丢事件，
// 导致前端收不到授权弹窗、子会话只能干等到 permission_timeout。
type permissionSinkKeyType struct{}

// PermissionSink 是子会话授权请求直达前端的发送器类型。
// 由宿主（如 mindx daemon）注入：接收子会话的授权请求数据，
// 负责将渲染所需信息（工具名、原因、安全级别、参数、发起会话 ID）发送给前端。
// 返回后调用方仍会执行挂起等待逻辑（waitForPermissionDecision），授权决策路径不变。
type PermissionSink func(data events.PermissionPendingData)

// WithPermissionSink 将授权请求旁路发送器注入 ctx。
func WithPermissionSink(ctx context.Context, sink PermissionSink) context.Context {
	return context.WithValue(ctx, permissionSinkKeyType{}, sink)
}

// securityLevelString 将 events.SecurityLevel 转换为稳定、可读的字符串
// （"safe" / "sensitive" / "high_risk"），便于存储在 session.PendingPermission 上。
// 字符串形式比原始 int 更利于人工审计。
func securityLevelString(level events.SecurityLevel) string {
	switch level {
	case events.LevelSafe:
		return "safe"
	case events.LevelSensitive:
		return "sensitive"
	case events.LevelHighRisk:
		return "high_risk"
	default:
		return "unknown"
	}
}

// buildGrantToolContext 构造一个适合从 runtime 层调用 tools.PermissionRequired.Grant 的 ToolContext。
//
// Grant 只需要读取会话级状态（项目目录、会话目录、日志器）即可进行预检查。
// 这里故意保持最小化 —— 完整的 ToolContext 会在实际 Execute 调用时再创建。
func (rt *Runtime) buildGrantToolContext(ctx context.Context, sess *session.Session) context.Context {
	tc := &tools.ToolContext{
		EmitEvent:        nil, // Grant 不发送事件。
		Logger:           rt.logger,
		Session:          sess,
		SessionWhitelist: sess.Whitelist(),
	}
	return tools.WithToolContext(ctx, tc)
}

// checkPermissionGrants 对当前轮次的每个调用执行 PermissionRequired.Grant。
// 若任一工具返回 granted=false：
//
//   - 将调用保存到 session.PendingPermission，以便用户后续的魔法词可以解析它。
//   - 发送 PermissionPending 事件，供 UI 渲染允许/拒绝对话框。
//   - 返回 pending 结构，调用方据此停止循环并传播终止元数据。
//
// 工具不会真正执行。这与旧的执行器级 "permission_required" 占位符的关键区别在于：
// 大模型永远看不到"需要授权"的文本，只能看到最终工具结果（允许则成功，拒绝则"Permission Denied"）。
//
// 当所有工具都不实现 PermissionRequired 或都返回 granted=true 时返回 nil。
func (rt *Runtime) checkPermissionGrants(
	ctx context.Context,
	b *AskBuilder,
	invocs []hooks.ToolCallInvocation,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) *session.PendingPermission {
	if len(invocs) == 0 {
		return nil
	}
	grantCtx := rt.buildGrantToolContext(ctx, b.session)

	for _, inv := range invocs {
		tool, ok := rt.toolReg.Get(inv.Name)
		if !ok {
			continue
		}
		pr, ok := tool.(tools.PermissionRequired)
		if !ok {
			// 工具未选择授权流程 —— 让 Execute 处理输入校验。
			continue
		}

		granted, reason := pr.Grant(grantCtx, inv.Arguments)
		if granted {
			continue
		}

		info := tool.Info()
		pending := &session.PendingPermission{
			ToolName:      inv.Name,
			ToolCallID:    inv.ID,
			Arguments:     inv.Arguments,
			Reason:        reason,
			SecurityLevel: securityLevelString(info.SecurityLevel),
		}
		b.session.SetPendingPermission(*pending)
		// 子智能体授权冒泡：仅子会话携带发起授权请求的会话 ID（子会话 ID）。
		// 前端在子会话授权弹窗点击允许/拒绝时据此发送带目标魔法词，
		// 后端精确路由，避免多个子会话并发挂起时决策错位。
		// 主会话自身的授权请求不设置（为空），保持旧行为——
		// 否则主会话授权弹窗的响应会被前端误判为子会话事件。
		permissionData := events.PermissionPendingData{
			TickID:        uuid.New().String(),
			ToolName:      inv.Name,
			Params:        inv.Arguments,
			Reason:        reason,
			SecurityLevel: info.SecurityLevel,
		}
		if b.permissionCh != nil {
			permissionData.SessionID = b.session.ID()
			// 子智能体授权冒泡：授权请求优先经旁路发送器直达前端，
			// 不依赖父 exec EventBus 的存活（父 exec 被取消/结束后订阅销毁，
			// 原 parentEmit 转发链路会静默丢事件，前端收不到授权弹窗）。
			// 未注入旁路（如测试环境）时退回原转发链路，行为保持不变。
			if sink := b.permissionSink; sink != nil {
				sink(permissionData)
			} else {
				emit(events.PermissionPending, permissionData)
			}
		} else {
			// 主会话自身的授权请求不设置 SessionID（为空），保持旧行为。
			emit(events.PermissionPending, permissionData)
		}
		logger.Info("需要授权",
			"tool", inv.Name,
			"reason", reason,
			"session", b.session.ID(),
		)
		return pending
	}
	return nil
}

// resolvePermissionMagicWord 检查新的用户消息。如果匹配 "PermissionAllow" /
// "PermissionDeny" 魔法词且会话存在待处理授权，则解析该授权并将相应工具结果追加到会话。
// 用户消息本身不会被追加 —— 大模型只能看到工具结果。
//
// 返回值：
//   - consumed：魔法词已被消费（调用方应跳过自己的用户消息追加）；
//     false 表示用户消息不是魔法词，或虽是魔法词但没有待处理授权。
//   - routedToSub：魔法词仅被转发到子会话（纯路由，主会话没有执行任何工具，
//     也没有新的工具结果追加）。调用方应直接结束本轮 exec，避免白跑一次 LLM 调用。
func (rt *Runtime) resolvePermissionMagicWord(
	ctx context.Context,
	b *AskBuilder,
	toolExec tools.ToolExecutor,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) (consumed bool, routedToSub bool) {
	mw := tools.ClassifyMagicWord(b.question)
	if mw.Action == "" {
		return false, false
	}

	// 带目标的魔法词必然来自子会话授权弹窗（前端点击对应子会话的允许/拒绝）：
	// 直接精确路由到目标子会话，且绝不触碰本会话（主会话）的挂起授权——
	// 否则主会话自身有 pending 时，取走 pending 会导致其授权丢失。
	// 无论目标子会话是否仍挂起，主会话都无需 LLM 响应，一律静默消费。
	if mw.SessionID != "" {
		if rt.subAgents != nil && rt.subAgents.dispatchPermission(mw.Action, mw.SessionID) {
			logger.Info("魔法词已路由到子智能体",
				"action", mw.Action, "session", b.session.ID(), "target", mw.SessionID)
		} else {
			// 目标子会话可能已授权超时或结束：静默消费，不追加到本会话，
			// 避免污染主会话上下文。
			logger.Info("带目标的魔法词无匹配的子会话，静默消费",
				"action", mw.Action, "session", b.session.ID(), "target", mw.SessionID)
		}
		return true, true
	}

	// 无目标魔法词：优先解析本会话（主会话）的挂起授权，否则按先到先服务
	// 路由到最早挂起的子会话（与旧行为一致）。
	pending := b.session.TakePendingPermission()
	if pending == nil {
		if rt.subAgents != nil && rt.subAgents.dispatchPermission(mw.Action, "") {
			logger.Info("魔法词已路由到子智能体",
				"action", mw.Action, "session", b.session.ID())
			// 主会话无 pending，魔法词被转发给子会话：同样无需 LLM 响应。
			return true, true
		}
		// 魔法词但没有待处理授权 —— 按普通用户轮次处理。
		return false, false
	}

	logger.Info("接收到授权回应",
		"action", mw.Action,
		"tool", pending.ToolName,
		"tool_call_id", pending.ToolCallID,
		"session", b.session.ID(),
	)

	rt.applyPermissionAction(ctx, b, pending, mw.Action, toolExec, emit, logger)
	// 授权动作执行了工具或合成拒绝结果并追加到会话，主会话需继续 LLM 循环
	// 消化新信息。
	return true, false
}

// applyPermissionAction 根据用户授权动作执行挂起工具或合成拒绝结果。
// 三种动作：
//   - PermissionAllow：直接执行挂起工具并追加结果。
//   - PermissionAllowSession：先加入会话白名单再执行挂起工具。
//   - PermissionDeny：合成"权限被拒绝"工具结果。
//
// 主会话魔法词路径与子智能体授权冒泡路径（waitForPermissionDecision）共用此逻辑，
// 保证两种场景下授权工具的执行与拒绝文案完全一致。
func (rt *Runtime) applyPermissionAction(
	ctx context.Context,
	b *AskBuilder,
	pending *session.PendingPermission,
	action string,
	toolExec tools.ToolExecutor,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) {
	switch action {
	case tools.PermissionAllow:
		rt.executePendingAndAppend(ctx, b, pending, toolExec, emit, logger)
	case tools.PermissionAllowSession:
		// 执行前先加入会话白名单。
		if entry := rt.buildWhitelistEntry(pending, b.session); entry != "" {
			if err := b.session.AddToWhitelist(pending.ToolName, entry); err != nil {
				logger.Error("添加到会话白名单失败", err)
			}
		}
		rt.executePendingAndAppend(ctx, b, pending, toolExec, emit, logger)
	case tools.PermissionDeny:
		rt.appendDeniedResult(ctx, b, pending)
	}
}

// permissionWaitTimeout 是子智能体等待主会话授权决策的超时时间。
// 超时后子会话以 permission_timeout 终止并写入终止标记，
// CollectResults 据此快速判定失败，而不是死等到默认 30 分钟收集超时。
const permissionWaitTimeout = 10 * time.Minute

// waitForPermissionDecision 挂起子智能体的执行循环，等待主会话（用户）的授权决策。
//
// 子智能体授权冒泡的核心：子会话遇到需要授权的工具时，不在本会话内以
// permission_pending 终止（那样子会话就结束了，主会话的授权无处可去），
// 而是通过 permissionCh 挂起，并由 subAgentManager 登记到挂起队列。
// 主会话收到用户魔法词后经 dispatchPermission 将授权决策送入本通道。
//
// 授权到达后执行挂起工具（Allow / AllowSession）或合成拒绝结果（Deny），
// 子会话继续执行循环；超时则以 permission_timeout 终止，避免无限挂起。
func (rt *Runtime) waitForPermissionDecision(
	ctx context.Context,
	b *AskBuilder,
	pending *session.PendingPermission,
	toolExec tools.ToolExecutor,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) {
	// 登记挂起：主会话的魔法词解析据此定位本子会话并路由授权决策。
	rt.subAgents.registerPermissionWait(b.session.ID(), b.permissionCh)
	defer rt.subAgents.clearPermissionWait(b.session.ID(), b.permissionCh)

	// 清除会话级待处理授权：pending 已由本函数接管（授权决策经 permissionCh 送达）。
	// 不清理则子会话在授权后残留旧 pending——主会话路径由 resolvePermissionMagicWord
	// 的 TakePendingPermission 清除，子会话冒泡路径必须同样清除，
	// 否则会话被复用（延续上下文）后状态不清，且若新任务问题恰好是魔法词会被误消费。
	b.session.TakePendingPermission()

	timeout := time.NewTimer(permissionWaitTimeout)
	defer timeout.Stop()

	logger.Info("子智能体循环挂起: 等待主会话授权",
		"session", b.session.ID(),
		"tool", pending.ToolName,
		"timeout", permissionWaitTimeout.String(),
	)

	select {
	case <-ctx.Done():
		b.resultErr = ctx.Err()
		b.resultTerminationReason = "cancelled"
	case sig := <-b.permissionCh:
		logger.Info("子智能体收到授权决策",
			"session", b.session.ID(),
			"action", sig.action,
			"tool", pending.ToolName,
		)
		rt.applyPermissionAction(ctx, b, pending, sig.action, toolExec, emit, logger)
	case <-timeout.C:
		logger.Warn("子智能体授权等待超时，终止执行",
			"session", b.session.ID(),
			"tool", pending.ToolName,
			"timeout", permissionWaitTimeout.String(),
		)
		b.resultTerminationReason = "permission_timeout"
	}
}

// buildWhitelistEntry 根据工具名称和参数构造白名单条目值。
//
// 返回要存储的条目（例如 bash 的基础命令名、write/edit 的解析后文件路径、
// run_script 的脚本路径），如果无法推导出有意义条目则返回 ""（此时不做白名单处理）。
func (rt *Runtime) buildWhitelistEntry(pending *session.PendingPermission, sess *session.Session) string {
	switch strings.ToLower(pending.ToolName) {
	case "bash":
		cmd, _ := pending.Arguments["command"].(string)
		if cmd == "" {
			return ""
		}
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			return ""
		}
		return parts[0]
	case "write", "edit", "read":
		// 兼容两种 schema 键：Write/Read 用 filePath，Edit 用 file_path。
		// GetParam 的变体生成是 snake_case→camelCase 单向的，无法从 filePath
		// 反向推导出 file_path，故需显式检查两个键。
		rawPath, ok := tools.GetParam(pending.Arguments, "filePath")
		if !ok {
			rawPath, ok = tools.GetParam(pending.Arguments, "file_path")
		}
		path, _ := rawPath.(string)
		if !ok || path == "" {
			return ""
		}
		resolved, _ := tools.ResolveTargetPath(path, sess.ProjectDir(), sess.SessionDir())
		return resolved
	case "ls":
		rawPath, ok := tools.GetParam(pending.Arguments, "path")
		path, _ := rawPath.(string)
		if !ok || path == "" {
			return ""
		}
		resolved, _ := tools.ResolveTargetPath(path, sess.ProjectDir(), sess.SessionDir())
		return resolved
	case "run_script":
		rawCmd, ok := tools.GetParam(pending.Arguments, "command")
		cmd, _ := rawCmd.(string)
		if !ok || cmd == "" {
			return ""
		}
		rawWD, _ := tools.GetParam(pending.Arguments, "working_dir")
		workingDir, _ := rawWD.(string)
		if workingDir == "" {
			workingDir = "."
		}
		// 与 RunScript.Grant 一致：基于项目目录将工作目录解析为绝对路径，
		// 否则相对 working_dir 下白名单条目永不命中绝对路径前缀匹配。
		workingDir, _ = tools.ResolveTargetPath(workingDir, sess.ProjectDir(), sess.SessionDir())
		if workingDir == "" {
			workingDir = "."
		}
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			return ""
		}
		// 确定脚本路径。如果第一个 token 看起来像解释器名（不含路径分隔符），则使用第二个 token。
		scriptCandidate := parts[0]
		if len(parts) > 1 && !strings.ContainsAny(parts[0], "./\\") {
			scriptCandidate = parts[1]
		}
		// 解析为绝对路径，与 RunScript.Grant() 行为保持一致。
		if !filepath.IsAbs(scriptCandidate) {
			scriptCandidate = filepath.Join(workingDir, scriptCandidate)
		}
		absScript, err := filepath.Abs(scriptCandidate)
		if err != nil {
			return ""
		}
		return filepath.Clean(absScript)
	default:
		return ""
	}
}

// executePendingAndAppend 真正运行被挂起的授权工具，然后将工具结果消息追加到会话，
// 使用**原始**的 tool_call_id。大模型永远只能看到工具结果，而看不到"等待人工审批"的中间状态。
func (rt *Runtime) executePendingAndAppend(
	ctx context.Context,
	b *AskBuilder,
	pending *session.PendingPermission,
	toolExec tools.ToolExecutor,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) {
	// 构造 ToolContext，使工具在执行期间可以访问会话信息
	//（例如文件工具解析项目相对路径）。
	execCtx := rt.buildGrantToolContext(ctx, b.session)
	execCtx, cancel := context.WithTimeout(execCtx, rt.syncTimeout)
	defer cancel()

	start := time.Now()
	emit(events.ToolExecStart, events.ToolExecStartData{
		ToolName: pending.ToolName,
		Params:   pending.Arguments,
	})

	tr := hooks.ToolResult{
		ToolName:   pending.ToolName,
		ToolCallID: pending.ToolCallID,
		Duration:   time.Since(start),
	}
	beforeProduced := false

	// ToolHook.Before 链（与正常执行路径 executeSingleTool 一致）。
	// 授权后真正执行的工具同样必须经过 Before 链——例如 FileModifyHook 在
	// Write/Edit 执行前进行文件备份与修改追踪，若跳过会导致备份被静默遗漏。
	for _, h := range rt.toolHooks {
		hr := h.Before(b.session.ID(), pending.ToolName, pending.Arguments)
		if hr.SkipWithResult != nil {
			tr = *hr.SkipWithResult
			tr.ToolCallID = pending.ToolCallID
			tr.Duration = time.Since(start)
			beforeProduced = true
			break
		}
		if hr.Abort {
			tr = failedToolResult(pending.ToolName, pending.ToolCallID, hr.AbortReason, start)
			beforeProduced = true
			break
		}
		if hr.Error != nil {
			tr = failedToolResult(pending.ToolName, pending.ToolCallID, hr.Error.Error(), start)
			beforeProduced = true
			break
		}
	}

	if !beforeProduced {
		execResult, execErr := toolExec.Execute(execCtx, pending.ToolName, pending.Arguments)
		tr.Duration = time.Since(start)
		if execErr != nil {
			tr.Error = execErr.Error()
			tr.Success = false
		} else if execResult != nil {
			tr.Result = execResult.Result
			tr.Images = execResult.Images
			tr.Success = execResult.Error == nil
			if execResult.Error != nil {
				tr.Error = execResult.Error.Error()
			}
		}

		// ToolHook.After 链（与正常执行路径 executeSingleTool 一致）。
		// 授权后真正执行的工具同样可能返回图片（例如白名单外文件的图片读取），
		// 必须经 ImageHook 转换为 ImageBlocks，否则图片会在授权路径上被静默丢弃。
		for _, h := range rt.toolHooks {
			hr := h.After(&tr)
			if hr.Abort {
				tr = failedToolResult(pending.ToolName, pending.ToolCallID, hr.AbortReason, start)
				break
			}
			if hr.Error != nil {
				tr = failedToolResult(pending.ToolName, pending.ToolCallID, hr.Error.Error(), start)
				break
			}
		}
	}

	// 与正常执行路径共用 formatToolResult，保证错误前缀、空结果引导等文案完全一致。
	content := formatToolResult(tr)
	logger.Info("工具执行结果 (Allow)",
		"tool", pending.ToolName,
		"tool_call_id", pending.ToolCallID,
		"duration_ms", tr.Duration.Milliseconds(),
		"session", b.session.ID(),
	)
	emit(events.ToolExecEnd, events.ToolExecEndData{
		ToolName:   pending.ToolName,
		ToolCallID: pending.ToolCallID,
		Duration:   tr.Duration,
		Success:    tr.Success,
		Result:     content,
	})

	if err := b.session.Append(ctx, session.Message{
		Role: "tool", Content: content, Timestamp: time.Now().Unix(),
		ToolCallID: pending.ToolCallID,
	}); err != nil {
		logger.Error("追加授权工具结果失败", err, "session", b.session.ID(), "tool_call_id", pending.ToolCallID)
		emit(events.Error, fmt.Sprintf("追加授权工具结果失败: %v", err))
		b.resultErr = fmt.Errorf("追加授权工具结果失败: %w", err)
		b.resultTerminationReason = "error"
	}

	// 图片消息：ImageHook 已把工具返回的图片转换为图片内容块。
	// 与正常执行路径一致，图片以 image_url 消息（user 角色）进入上下文，
	// 紧随对应的工具结果之后，而非混入工具结果的文本内容。
	if len(tr.ImageBlocks) > 0 {
		imgMsg := session.Message{
			Role: "user", Timestamp: time.Now().Unix(),
			Content: "以下是工具 " + pending.ToolName + " 读取到的图片内容（视觉消息），请结合图片进行分析：",
			Images:  tr.ImageBlocks,
		}
		if err := b.session.Append(ctx, imgMsg); err != nil {
			logger.Error("追加授权工具图片消息失败", err, "session", b.session.ID(), "tool_call_id", pending.ToolCallID)
			emit(events.Error, fmt.Sprintf("追加授权工具图片消息失败: %v", err))
			b.resultErr = fmt.Errorf("追加授权工具图片消息失败: %w", err)
			b.resultTerminationReason = "error"
			return
		}
		logger.Info("图片已作为视觉消息追加（授权路径）",
			"session", b.session.ID(), "tool", pending.ToolName, "images", len(tr.ImageBlocks))
	}
}

// cleanDeniedReason 清理授权被拒时给模型的授权原因：
// 剥离魔法词使用说明（用户已明确拒绝，无需再提示授权方式）与越权说明，
// 仅保留"为什么需要授权"的核心描述（如越界路径），并截断超长部分，
// 避免重复陈述、泄露授权方式浪费 Token。
func cleanDeniedReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if i := strings.Index(reason, "（可用 Permission"); i >= 0 {
		reason = reason[:i]
	}
	if i := strings.Index(reason, "。这是越权操作"); i >= 0 {
		reason = reason[:i]
	}
	if i := strings.Index(reason, "\n"); i >= 0 {
		reason = reason[:i]
	}
	reason = strings.TrimSpace(reason)
	return tools.TruncateString(reason, 120)
}

// appendDeniedResult 合成一个引导式的"权限被拒绝"工具结果，并连同原始 tool_call_id
// 一起追加到会话。大模型永远只看到这条结果，而看不到"用户拒绝"的中间状态。
// 文案采用第一人称引导格式（我做了什么 → 原因 → 下一步我应该怎么做）：
// 用户明确表示不允许授权，模型应据此调整执行思路（换路径/换工具），
// 而不是继续尝试被拒的方式。
func (rt *Runtime) appendDeniedResult(
	ctx context.Context,
	b *AskBuilder,
	pending *session.PendingPermission,
) {
	reason := cleanDeniedReason(pending.Reason)
	if reason == "" {
		reason = "未给出具体原因"
	}
	content := fmt.Sprintf(
		"我尝试执行需要授权的操作（工具 %s，原因：%s），但用户明确表示不允许授权。\n"+
			"原因：该操作被用户拒绝。\n"+
			"下一步我应该：考虑其它的执行路径或工具来完成此工作，而不是继续尝试该方式。",
		pending.ToolName, reason,
	)
	if err := b.session.Append(ctx, session.Message{
		Role: "tool", Content: content, Timestamp: time.Now().Unix(),
		ToolCallID: pending.ToolCallID,
	}); err != nil {
		rt.logger.Error("追加拒绝结果失败", err, "session", b.session.ID(), "tool_call_id", pending.ToolCallID)
		b.resultErr = fmt.Errorf("追加拒绝结果失败: %w", err)
		b.resultTerminationReason = "error"
	}
}

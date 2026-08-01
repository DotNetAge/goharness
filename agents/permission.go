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
		emit(events.PermissionPending, events.PermissionPendingData{
			TickID:        uuid.New().String(),
			ToolName:      inv.Name,
			Params:        inv.Arguments,
			Reason:        reason,
			SecurityLevel: info.SecurityLevel,
		})
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
// 返回 true 表示魔法词已被消费（调用方应跳过自己的用户消息追加）；
// 返回 false 表示：用户消息不是魔法词，或虽是魔法词但没有待处理授权。
func (rt *Runtime) resolvePermissionMagicWord(
	ctx context.Context,
	b *AskBuilder,
	toolExec tools.ToolExecutor,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) bool {
	action := tools.ClassifyMagicWord(b.question)
	if action == "" {
		return false
	}
	pending := b.session.TakePendingPermission()
	if pending == nil {
		// 魔法词但没有待处理授权 —— 按普通用户轮次处理。
		return false
	}

	logger.Info("接收到授权回应",
		"action", action,
		"tool", pending.ToolName,
		"tool_call_id", pending.ToolCallID,
		"session", b.session.ID(),
	)

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
	return true
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

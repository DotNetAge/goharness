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
//（"safe" / "sensitive" / "high_risk"），便于存储在 session.PendingPermission 上。
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
	switch pending.ToolName {
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
	case "write", "edit":
		path, _ := pending.Arguments["filePath"].(string)
		if path == "" {
			return ""
		}
		resolved, _ := tools.ResolveTargetPath(path, sess.ProjectDir(), sess.SessionDir())
		return resolved
	case "run_script":
		cmd, _ := pending.Arguments["command"].(string)
		if cmd == "" {
			return ""
		}
		workingDir, _ := pending.Arguments["working_dir"].(string)
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
		return filepath.Clean(scriptCandidate)
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
	execResult, execErr := toolExec.Execute(execCtx, pending.ToolName, pending.Arguments)
	duration := time.Since(start)

	var content string
	if execErr != nil {
		content = fmt.Sprintf("[%s] 错误: %s", pending.ToolName, execErr.Error())
	} else if execResult != nil {
		if execResult.Error != nil {
			content = fmt.Sprintf("[%s] 错误: %s", pending.ToolName, execResult.Error.Error())
		} else if execResult.Result != "" {
			content = execResult.Result
		} else {
			content = fmt.Sprintf("[%s] 返回: (空结果)", pending.ToolName)
		}
	} else {
		content = fmt.Sprintf("[%s] 返回: (无结果)", pending.ToolName)
	}
	logger.Info("工具执行结果 (Allow)",
		"tool", pending.ToolName,
		"tool_call_id", pending.ToolCallID,
		"duration_ms", duration.Milliseconds(),
		"session", b.session.ID(),
	)
	emit(events.ToolExecEnd, events.ToolExecEndData{
		ToolName:   pending.ToolName,
		ToolCallID: pending.ToolCallID,
		Duration:   duration,
		Success:    execErr == nil,
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
}

// appendDeniedResult 合成一个"权限被拒绝"的工具结果，并连同原始 tool_call_id
// 一起追加到会话。大模型永远只看到这条结果，而看不到"用户拒绝"的中间状态，
// 因此它可以自行调整方案（换一条路、询问用户等）。
func (rt *Runtime) appendDeniedResult(
	ctx context.Context,
	b *AskBuilder,
	pending *session.PendingPermission,
) {
	reason := pending.Reason
	if reason == "" {
		reason = "用户拒绝"
	}
	content := fmt.Sprintf("权限被拒绝：%s", reason)
	if err := b.session.Append(ctx, session.Message{
		Role: "tool", Content: content, Timestamp: time.Now().Unix(),
		ToolCallID: pending.ToolCallID,
	}); err != nil {
		rt.logger.Error("追加拒绝结果失败", err, "session", b.session.ID(), "tool_call_id", pending.ToolCallID)
		b.resultErr = fmt.Errorf("追加拒绝结果失败: %w", err)
		b.resultTerminationReason = "error"
	}
}

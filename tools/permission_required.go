package tools

import (
	"context"
	"strings"
)

// PermissionRequired 是一个可选实现接口，工具可通过实现它来声明其执行需要运行时权限校验。
//
// # 为什么是可选实现？
//
// 绝大多数工具（Read、Grep、Glob、Ls、WebSearch、WebFetch、
// AskUser、CollectResults、TaskCreate、TaskGet、TaskList、TaskUpdate、
// TeamCreate、TeamDelete、TeamGetTasks、TeamList、Skill、Sleep）的
// SecurityLevel = LevelSafe / IsReadOnly = true，可以在不打扰用户的情况下直接调用。
// 只有一小部分工具 —— Bash、RunScript、Write、Edit —— 会以可能损害系统的方式
// 访问文件系统或 shell，因此它们通过实现该接口来选择加入权限校验。
//
// # 工作原理
//
// 运行时会在工具执行*之前*调用 Grant(ctx, params)：
//
//   - granted == true  → 工具安全；运行时直接执行它。
//   - granted == false → 工具需要用户批准。运行时发出
//                        PermissionPending 事件，将该调用保存到
//                        session.PendingPermission 中，并停止思考循环。
//                        用户通过新的 Ask() 调用以"魔法词"（参见下文的
//                        PermissionAllow / PermissionDeny）作出响应。
//                        随后运行时要么执行工具（Allow），要么在继续循环前
//                        追加一条"Permission Denied"工具结果（Deny）。
//
// # 魔法词与 AskUser 的区别
//
// AskUser 的响应（例如"选项 A"）本身就是 LLM 上下文的一部分：
// 助手的下一轮既能看到问题，也能看到用户的回答。
//
// 权限校验对 LLM 是不可见的：魔法词"PermissionAllow" /
// "PermissionDeny"通过常规聊天通道到达，但在到达会话之前会被运行时过滤掉。
// LLM 只会看到工具结果（成功或"Permission Denied"），永远不会看到
// "等待人工批准"的中间状态。
//
// # 实现
//
// 每个工具自行决定什么是"受限的"。典型信号：
//   - Bash：命中 dangerousPatterns，或基础命令不在白名单中。
//   - Write / Edit：解析后的路径位于项目/会话边界之外。
//   - RunScript：脚本路径位于技能根目录之外，或解释器不在平台支持列表中。
//
// 硬性错误（如 .env、.ssh 等敏感文件）不应通过 Grant() 表达——
// 应保留为 Execute() 中的错误。Grant() 仅表达"询问，但用户可覆盖"。
type PermissionRequired interface {
	// Grant 检查工具输入并返回工具是否可以在不询问用户的情况下继续执行。
	//
	// 参数：
	//   - ctx:    标准 context.Context（无需截止时间——运行时会
	//            在用户中止时处理取消）。
	//   - params: 将传递给 Execute 的同一参数 map。
	//            工具应将其视为只读；若工具需要规范化输入
	//            （例如解析 "session:" 前缀），应在 Execute() 中重新执行该解析，
	//            而不是在此处修改 params。
	//
	// 返回：
	//   - granted: true 表示无需权限校验；false 表示触发权限校验流程。
	//   - reason:  在向用户询问时显示在 UI 中的人类可读说明。
	//              当 granted 为 true 时被忽略。应描述触发校验的具体内容
	//              （例如"命令包含 'rm -rf /'"、"路径位于项目边界之外"），
	//              而不仅仅是"这需要批准"。
	Grant(ctx context.Context, params map[string]any) (granted bool, reason string)
}

// ImplementsPermissionRequired 报告工具是否可选实现了
// PermissionRequired 接口。未实现该接口的工具被运行时视为"始终允许"。
func ImplementsPermissionRequired(tool FuncTool) bool {
	_, ok := tool.(PermissionRequired)
	return ok
}

// 魔法词常量，由 UI 用于响应 PermissionPending 事件。运行时将这些常量作为
// 控制信号拦截：该消息永远不会被追加到会话中，运行时会立即解决挂起的权限
// （在 Allow 时执行工具，或在 Deny 时追加"Permission Denied"结果），
// 然后继续循环。
//
// 格式为无前缀的纯单词，以便 UI 在聊天框中以纯文本形式渲染。魔法词检测
// 会去除首尾空白且不区分大小写。
const (
	// PermissionAllow 由 UI 发送，用于批准挂起的权限并以原始参数运行工具。
	PermissionAllow = "PermissionAllow"

	// PermissionAllowSession 由 UI 在用户勾选"记住本次会话的选择"时发送。
	// 其作用与 PermissionAllow 相同——工具会被执行——但同时会将该工具及其
	// 已批准的参数加入会话级白名单（{sessionDir}/session-wl.json）。
	// 后续以匹配参数调用同一工具时将自动放行，无需用户确认。
	PermissionAllowSession = "PermissionAllowSession"

	// PermissionDeny 由 UI 发送，用于拒绝挂起的权限。
	// LLM 会看到一条"Permission Denied"工具结果，并可据此调整计划
	// （例如询问用户、选择不同路径等）。
	PermissionDeny = "PermissionDeny"
)

// ClassifyMagicWord 在去除首尾空白后返回用户消息所暗示的魔法词动作，
// 若该消息不是魔法词则返回 ""。
//
// 检测范围被刻意收窄——核心目的是让权限校验流程对 LLM 不可见。
// 除与 PermissionAllow / PermissionDeny 精确匹配（去除空白、不区分大小写）外，
// 其他任何内容都被视为普通用户消息。
func ClassifyMagicWord(msg string) string {
	trimmed := strings.TrimSpace(msg)
	switch {
	case strings.EqualFold(trimmed, PermissionAllow):
		return PermissionAllow
	case strings.EqualFold(trimmed, PermissionAllowSession):
		return PermissionAllowSession
	case strings.EqualFold(trimmed, PermissionDeny):
		return PermissionDeny
	default:
		return ""
	}
}

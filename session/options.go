package session

import (
	"context"

	"github.com/DotNetAge/goharness/sandbox"
)

// SessionConfig 是用于配置 Session 实例的函数式选项。
// 此模式允许灵活、可读的配置，且在新增选项时不会引入破坏性变更。
//
// 示例：
//
//	session := NewSession("id", "agent",
//	    WithModelContextResolver(func() int64 { return model.ContextLength }),
//	    WithCompactionHandler(myHandler),
//	)
type SessionConfig func(*Session)

func (s *Session) logInfo(msg string, keyvals ...any) {
	if s.log != nil {
		s.log.Info(msg, keyvals...)
	}
}

func (s *Session) logError(msg string, err error, keyvals ...any) {
	if s.log != nil {
		s.log.Error(msg, err, keyvals...)
	}
}

// WithSummarySink 注入压缩摘要的交付回调（依赖倒置：摘要存哪、存成什么格式由应用层决定）。
//
// 在 cursor 移动前同步调用，返回 error 则本次压缩视为失败（cursor 不移动，
// 防止丢历史不留记忆）。未注入时压缩视为失败。
func WithSummarySink(h func(ctx context.Context, summary string) error) SessionConfig {
	return func(s *Session) { s.summarySink = h }
}

// WithCompactionPrompt 覆盖压缩指令（空 = 使用压缩器内置默认）。
// 供应用层按 Agent 特点定制摘要重点（如代码会话侧重决策路径、客服会话侧重用户诉求）。
func WithCompactionPrompt(prompt string) SessionConfig {
	return func(s *Session) { s.compactionPrompt = prompt }
}

// WithTeam 为会话实例设置绑定的团队名（空 = 个人会话）。
// 供 New 路径直接指定；Load 路径的团队绑定从会话元数据自动恢复。
func WithTeam(teamName string) SessionConfig {
	return func(s *Session) { s.teamName = teamName }
}

// WithCompactor 注入压缩器（依赖倒置，由 agents 层实现）。
//
// Compactor 负责构造与主对话请求字段一致的 LLM 调用（system + tools + messages 前缀
// 逐 token 一致，仅末尾追加压缩指令），以命中 KV 前缀缓存。未注入时压缩跳过摘要生成。
func WithCompactor(c Compactor) SessionConfig {
	return func(s *Session) { s.compactor = c }
}

// WithModelContextResolver 注入一个回调，用于动态查询当前会话使用的
// 模型的上下文窗口大小（ContextLength）。
//
// 每次需要窗口大小时（压缩触发判定、ContextUsage 计算等）都会调用此回调，
// 保证用户切换模型后窗口大小立即更新——窗口大小是模型能力的函数，
// 不是会话的固定属性。
//
// 回调未注入或返回 0 时禁用自动压缩。
func WithModelContextResolver(fn func() int64) SessionConfig {
	return func(s *Session) { s.modelContextResolver = fn }
}

// WithSandbox 注入会话级逻辑沙箱。
// 沙箱为工具提供统一的文件/网络/命令安全决策。
// 传入 nil 等同于不调用（沙箱未启用，工具回退到各自的安全检查）。
//
// 沙箱实例应为已通过 sandbox.NewSandbox 构造的 *sandbox.Sandbox。
// 策略热更新通过 sb.UpdatePolicy() 进行，无需重建会话。
func WithSandbox(sb *sandbox.Sandbox) SessionConfig {
	return func(s *Session) { s.sandbox = sb }
}

// WithCompactionHandler 设置在每次压缩事件后调用的回调函数。
// 可用于日志记录、指标采集或 UI 更新。
func WithCompactionHandler(h func(CompactionEvent)) SessionConfig {
	return func(s *Session) { s.compactionHandler = h }
}

// WithCompactStartHandler 注入 TryCompact 开始基于 LLM 摘要压缩前的回调。
// 回调接收 (windowTokens, maxWindowSize)。传 nil 以禁用。
func WithCompactStartHandler(h func(windowTokens, maxWindowSize int64)) SessionConfig {
	return func(s *Session) { s.compactStartHandler = h }
}

// WithCompactDoneHandler 注入 TryCompact 完成后的回调。
// 回调接收 (messagesSlid, windowTokens)。传 nil 以禁用。
func WithCompactDoneHandler(h func(messagesSlid int, windowTokens int64)) SessionConfig {
	return func(s *Session) { s.compactDoneHandler = h }
}

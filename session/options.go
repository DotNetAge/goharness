package session

import "github.com/DotNetAge/goharness/sandbox"

// SessionConfig is a functional option for configuring Session instances.
// This pattern allows for flexible, readable configuration without breaking changes
// when new options are added.
//
// Example:
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

// WithMemory configures the memory store for compaction chunks.
// If not set, an in-memory store is used by default.
func WithMemory(mem MemoryStore) SessionConfig {
	return func(s *Session) { s.mem = mem }
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

// WithCompactionHandler sets a callback function that is invoked after each
// compaction event. This can be used for logging, metrics, or UI updates.
func WithCompactionHandler(h func(CompactionEvent)) SessionConfig {
	return func(s *Session) { s.compactionHandler = h }
}



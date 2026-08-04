package tools

import (
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
)

// executorConfig 是工具执行器的配置结构体。
//
// 权限校验有意不归执行器管——运行时在调用执行器之前会预检
// PermissionRequired.Grant()，而未接入权限接口的工具则自行负责
// 输入校验。让执行器只关注"如何运行工具"，避免在两个层级
// 重复实现检查链模式。
type executorConfig struct {
	registry     ToolRegistry
	eventEmitter func(events.ReactEvent)
	sessionStore session.SessionStore
	kvStore      store.KVStore
	fileStore    store.FileStore
	session      *session.Session // 会话级状态的权威来源
	logger       logging.Logger
}

// ExecutorOption 是配置 ToolExecutor 的函数式选项。
type ExecutorOption func(*executorConfig)

// WithLogger 设置工具执行的日志器。
func WithLogger(logger logging.Logger) ExecutorOption {
	return func(c *executorConfig) { c.logger = logger }
}

// WithEventEmitter 设置工具执行的事件发射器。
func WithEventEmitter(emitter func(events.ReactEvent)) ExecutorOption {
	return func(c *executorConfig) { c.eventEmitter = emitter }
}

// WithSessionStore 设置用于加载子会话消息的 session store。
func WithSessionStore(store session.SessionStore) ExecutorOption {
	return func(c *executorConfig) { c.sessionStore = store }
}

// WithKVStore 设置 KV store。
func WithKVStore(store store.KVStore) ExecutorOption {
	return func(c *executorConfig) { c.kvStore = store }
}

// WithFileStore 设置 file store。
func WithFileStore(store store.FileStore) ExecutorOption {
	return func(c *executorConfig) { c.fileStore = store }
}

// WithSession 在执行器配置上设置 Session 指针。
// 工具通过 Session 指针访问会话属性（ID、ProjectDir、AgentName 等），
// 而非使用提取的副本。
func WithSession(s *session.Session) ExecutorOption {
	return func(c *executorConfig) { c.session = s }
}

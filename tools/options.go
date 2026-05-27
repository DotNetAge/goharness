package tools

import (
	"github.com/DotNetAge/goreact/events"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/store"
)

// executorConfig 是工具执行器的配置结构体。
// 包含执行器运行所需的所有依赖和配置项。
type executorConfig struct {
	registry          ToolRegistry           // 工具注册表
	permissionChecker ToolPermissionChecker  // 权限检查器
	eventEmitter      func(events.ReactEvent) // 事件发射函数
	resultStore       *store.ResultStore     // 结果存储（用于异步工具）
	kvStore           store.KVStore          // KV 存储（用于缓存）
	fileStore         store.FileStore        // 文件存储
	sessionID         string                 // 会话 ID
	logger            logging.Logger         // 日志记录器
	projectDir        string                 // 项目目录
	sessionDir        string                 // 会话目录
}

// ExecutorOption 是执行器选项函数类型。
// 用于函数式选项模式配置 ToolExecutor。
type ExecutorOption func(*executorConfig)

// WithPermissionChecker 创建设置权限检查器的选项。
//
// 参数：
//   - checker: 权限检查器实例
//
// 返回：
//   - ExecutorOption: 选项函数
func WithPermissionChecker(checker ToolPermissionChecker) ExecutorOption {
	return func(c *executorConfig) { c.permissionChecker = checker }
}

// WithLogger 创建设置日志记录器的选项。
//
// 参数：
//   - logger: 日志记录器实例
//
// 返回：
//   - ExecutorOption: 选项函数
func WithLogger(logger logging.Logger) ExecutorOption {
	return func(c *executorConfig) { c.logger = logger }
}

// WithEventEmitter 创建设置事件发射器的选项。
//
// 参数：
//   - emitter: 事件发射函数
//
// 返回：
//   - ExecutorOption: 选项函数
func WithEventEmitter(emitter func(events.ReactEvent)) ExecutorOption {
	return func(c *executorConfig) { c.eventEmitter = emitter }
}

// WithResultStore 创建设置结果存储的选项。
//
// 参数：
//   - store: 结果存储实例
//
// 返回：
//   - ExecutorOption: 选项函数
func WithResultStore(store *store.ResultStore) ExecutorOption {
	return func(c *executorConfig) { c.resultStore = store }
}

// WithKVStore 创建设置 KV 存储的选项。
//
// 参数：
//   - store: KV 存储实例
//
// 返回：
//   - ExecutorOption: 选项函数
func WithKVStore(store store.KVStore) ExecutorOption {
	return func(c *executorConfig) { c.kvStore = store }
}

// WithFileStore 创建设置文件存储的选项。
//
// 参数：
//   - store: 文件存储实例
//
// 返回：
//   - ExecutorOption: 选项函数
func WithFileStore(store store.FileStore) ExecutorOption {
	return func(c *executorConfig) { c.fileStore = store }
}

// WithExecutorSessionID 创建设置会话 ID 的选项。
//
// 参数：
//   - id: 会话标识符
//
// 返回：
//   - ExecutorOption: 选项函数
func WithExecutorSessionID(id string) ExecutorOption {
	return func(c *executorConfig) { c.sessionID = id }
}

// WithProjectDirExecutor 创建设置项目目录的选项。
//
// 参数：
//   - dir: 项目根目录路径
//
// 返回：
//   - ExecutorOption: 选项函数
func WithProjectDirExecutor(dir string) ExecutorOption {
	return func(c *executorConfig) { c.projectDir = dir }
}

// WithSessionDirExecutor 创建设置会话目录的选项。
//
// 参数：
//   - dir: 会话目录路径
//
// 返回：
//   - ExecutorOption: 选项函数
func WithSessionDirExecutor(dir string) ExecutorOption {
	return func(c *executorConfig) { c.sessionDir = dir }
}

package tools

import (
	"github.com/DotNetAge/goreact/events"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/store"
)

type executorConfig struct {
	registry          ToolRegistry
	permissionChecker ToolPermissionChecker
	eventEmitter      func(events.ReactEvent)
	resultStore       *store.ResultStore
	kvStore           store.KVStore
	fileStore         store.FileStore
	sessionID         string
	logger            logging.Logger
	projectDir        string
	sessionDir        string
}

type ExecutorOption func(*executorConfig)

func WithPermissionChecker(checker ToolPermissionChecker) ExecutorOption {
	return func(c *executorConfig) { c.permissionChecker = checker }
}

func WithLogger(logger logging.Logger) ExecutorOption {
	return func(c *executorConfig) { c.logger = logger }
}

func WithEventEmitter(emitter func(events.ReactEvent)) ExecutorOption {
	return func(c *executorConfig) { c.eventEmitter = emitter }
}

func WithResultStore(store *store.ResultStore) ExecutorOption {
	return func(c *executorConfig) { c.resultStore = store }
}

func WithKVStore(store store.KVStore) ExecutorOption {
	return func(c *executorConfig) { c.kvStore = store }
}

func WithFileStore(store store.FileStore) ExecutorOption {
	return func(c *executorConfig) { c.fileStore = store }
}

func WithExecutorSessionID(id string) ExecutorOption {
	return func(c *executorConfig) { c.sessionID = id }
}

func WithProjectDirExecutor(dir string) ExecutorOption {
	return func(c *executorConfig) { c.projectDir = dir }
}

func WithSessionDirExecutor(dir string) ExecutorOption {
	return func(c *executorConfig) { c.sessionDir = dir }
}

package tools

import (
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
)

// executorConfig is the configuration struct for the tool executor.
//
// Permission enforcement is intentionally NOT an executor concern — the
// runtime pre-checks PermissionRequired.Grant() before calling the
// executor, and tools that opt out of the permission interface are
// trusted to handle their own input validation. Keeping the executor
// focused on "how to run a tool" avoids duplicating the chain-of-checkers
// pattern at two layers.
type executorConfig struct {
	registry     ToolRegistry
	eventEmitter func(events.ReactEvent)
	resultStore  *store.ResultStore
	kvStore      store.KVStore
	fileStore    store.FileStore
	session      *session.Session // authoritative source for session-level state
	logger       logging.Logger
}

// ExecutorOption is a functional option for configuring ToolExecutor.
type ExecutorOption func(*executorConfig)

// WithLogger sets the logger for tool execution.
func WithLogger(logger logging.Logger) ExecutorOption {
	return func(c *executorConfig) { c.logger = logger }
}

// WithEventEmitter sets the event emitter for tool execution.
func WithEventEmitter(emitter func(events.ReactEvent)) ExecutorOption {
	return func(c *executorConfig) { c.eventEmitter = emitter }
}

// WithResultStore sets the result store for async tool execution.
func WithResultStore(store *store.ResultStore) ExecutorOption {
	return func(c *executorConfig) { c.resultStore = store }
}

// WithKVStore sets the KV store.
func WithKVStore(store store.KVStore) ExecutorOption {
	return func(c *executorConfig) { c.kvStore = store }
}

// WithFileStore sets the file store.
func WithFileStore(store store.FileStore) ExecutorOption {
	return func(c *executorConfig) { c.fileStore = store }
}

// WithSession sets the Session pointer on the executor config.
// Tools access session properties (ID, ProjectDir, AgentName, etc.)
// through the Session pointer rather than extracted copies.
func WithSession(s *session.Session) ExecutorOption {
	return func(c *executorConfig) { c.session = s }
}

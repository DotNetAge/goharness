package agents

import (
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/rule"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
	"github.com/DotNetAge/goharness/store"
	"github.com/DotNetAge/goharness/tools"
)

type RuntimeConfig func(*Runtime)

func WithModel(cfg config.ModelConfig) RuntimeConfig {
	return func(r *Runtime) { r.model = cfg }
}

func WithAgentRegistry(reg *config.AgentRegistry) RuntimeConfig {
	return func(r *Runtime) { r.agentReg = reg }
}

func WithProviderRegistry(reg config.ProviderRegistry) RuntimeConfig {
	return func(r *Runtime) { r.providerReg = reg }
}

func WithToolRegistry(reg tools.ToolRegistry) RuntimeConfig {
	return func(r *Runtime) { r.toolReg = reg }
}

func WithSkillRegistry(reg skill.SkillRegistry) RuntimeConfig {
	return func(r *Runtime) { r.skillReg = reg }
}

func WithRuleRegistry(reg rule.RuleRegistry) RuntimeConfig {
	return func(r *Runtime) { r.ruleReg = reg }
}

func WithMemory(mem memory.Memory) RuntimeConfig {
	return func(r *Runtime) { r.mem = mem }
}

func WithLogger(l logging.Logger) RuntimeConfig {
	return func(r *Runtime) { r.logger = l }
}

func WithLoopHooks(hh ...hooks.LoopHook) RuntimeConfig {
	return func(r *Runtime) { r.loopHooks = append(r.loopHooks, hh...) }
}

func WithToolHooks(hh ...hooks.ToolHook) RuntimeConfig {
	return func(r *Runtime) { r.toolHooks = append(r.toolHooks, hh...) }
}

// WithTokenUsageStore sets the token usage storage backend.
// The store persists usage records after each LLM streaming response.
// If not set, a NoopTokenUsageStore is used (no usage tracking).
func WithTokenUsageStore(store session.TokenUsageStore) RuntimeConfig {
	return func(r *Runtime) { r.tokenUsageStore = store }
}

// WithKVStore sets the session-scoped key-value storage backend.
// The store is injected into the ToolContext so task management tools
// (TaskCreate/TaskGet/TaskUpdate/TaskList) and other KV-aware tools
// can persist per-session state. If not set, those tools will return
// "KVStore not available" at execution time.
func WithKVStore(kv store.KVStore) RuntimeConfig {
	return func(r *Runtime) { r.kvStore = kv }
}

// WithResultStore sets the async task result store.
// The store is injected into the ToolContext so SubAgentTool can persist
// results of spawned sub-agents and CollectResultsTool can block-wait for
// them. If not set, CollectResults will return
// "collect_results tool requires ToolContext with ResultStore" and
// SubAgent results will not be retrievable.
func WithResultStore(rs *store.ResultStore) RuntimeConfig {
	return func(r *Runtime) { r.resultStore = rs }
}

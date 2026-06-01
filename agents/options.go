package agents

import (
	"github.com/DotNetAge/goreact/config"
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/logging"
	"github.com/DotNetAge/goreact/memory"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/session"
	"github.com/DotNetAge/goreact/tools"
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

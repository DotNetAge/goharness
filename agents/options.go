package agents

import (
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/rule"
	"github.com/DotNetAge/goharness/sandbox"
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
	return func(r *Runtime) { r.prompt.agentReg = reg }
}

func WithProviderRegistry(reg config.ProviderRegistry) RuntimeConfig {
	return func(r *Runtime) { r.providerReg = reg }
}

func WithToolRegistry(reg tools.ToolRegistry) RuntimeConfig {
	return func(r *Runtime) { r.toolReg = reg }
}

func WithSkillRegistry(reg skill.SkillRegistry) RuntimeConfig {
	return func(r *Runtime) { r.prompt.skillReg = reg }
}

func WithRuleRegistry(reg rule.RuleRegistry) RuntimeConfig {
	return func(r *Runtime) { r.prompt.ruleReg = reg }
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
// "KVStore 不可用" at execution time.
func WithKVStore(kv store.KVStore) RuntimeConfig {
	return func(r *Runtime) { r.kvStore = kv }
}

// WithSessionStore sets the session store for sub-session message loading.
// Used by CollectResults to recover SubAgent results from disk.
func WithSessionStore(ss session.SessionStore) RuntimeConfig {
	return func(r *Runtime) { r.sessionStore = ss }
}

// WithSkillsPrompt overrides the default skills catalog prompt section.
// The provided function receives the filtered list of skills for the current agent
// and should return the complete catalog string (or empty to omit the section).
// When nil (default), the built-in buildSkillsCatalog is used.
func WithSkillsPrompt(builder func(skills []*skill.Skill) string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.skillsCatalogBuilder = builder }
}

// WithEnvs overrides the default Environment section in system prompts.
// The provided function receives EnvsParams (SessionID, ProjectDir, SessionDir)
// and should return the complete environment section string (or empty to omit).
// When nil (default), the built-in buildEnvironmentInfo is used.
func WithEnvs(builder func(params EnvsParams) string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.envsBuilder = builder }
}

// WithSearchStrategy overrides the default Search Strategy section in system prompts.
// The provided function receives no parameters and should return the complete section
// string (or empty to omit the section entirely).
// When nil (default), the built-in buildSearchPriority is used.
func WithSearchStrategy(builder func() string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.searchStrategyBuilder = builder }
}

// WithLLMClient 设置自定义大语言模型客户端。
// 注入的客户端将替代默认的 gochat 实现，便于单元测试 mock 或多提供商切换。
func WithLLMClient(client LLMClient) RuntimeConfig {
	return func(r *Runtime) { r.llmClient = client }
}

// WithSandbox 注入会话级逻辑沙箱。
//
// 沙箱启用后，所有文件访问工具（Read/Edit/Write/Ls/Glob/RunScript/Grep）、
// 命令执行工具（Bash）和网络工具（WebFetch/WebSearch/SogouSearch/WeixinSearch）
// 均由沙箱统一做安全决策（Grant 阶段 Allow/Deny/AskUser，Execute 阶段 Enforce）。
//
// 沙箱实例会自动注入到 Runtime 创建的所有子 Agent 会话中。
// 主会话由调用方创建，需通过 rt.Sandbox() 获取沙箱实例并手动注入：
//
//	sb := sandbox.NewSandbox(policy, logger)
//	rt := agents.NewRuntime(agents.WithSandbox(sb))
//	// 主会话创建时注入：
//	sess, _ := session.New(..., session.WithSandbox(rt.Sandbox()))
//
// 沙箱未设置（nil）时，所有工具回退到旧逻辑（detectDangerousCommand 等）。
func WithSandbox(sb *sandbox.Sandbox) RuntimeConfig {
	return func(r *Runtime) { r.sandbox = sb }
}

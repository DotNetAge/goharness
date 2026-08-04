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

// WithTokenUsageStore 设置 token 用量存储后端。
// 每次大语言模型流式响应后，用量记录会被持久化到该存储。
// 若未设置，使用 NoopTokenUsageStore（不进行用量跟踪）。
func WithTokenUsageStore(store session.TokenUsageStore) RuntimeConfig {
	return func(r *Runtime) { r.tokenUsageStore = store }
}

// WithKVStore 设置会话级键值存储后端。
// 该存储注入到 ToolContext 中，使任务管理工具
//（TaskCreate/TaskGet/TaskUpdate/TaskList）及其他 KV 感知工具
// 可以持久化会话级状态。若未设置，这些工具在执行时返回
// "KVStore 不可用"。
func WithKVStore(kv store.KVStore) RuntimeConfig {
	return func(r *Runtime) { r.kvStore = kv }
}

// WithSessionStore 设置会话存储，用于子会话消息加载。
// CollectResults 用它从磁盘恢复 SubAgent 结果。
func WithSessionStore(ss session.SessionStore) RuntimeConfig {
	return func(r *Runtime) { r.sessionStore = ss }
}

// WithSkillsPrompt 覆盖默认的技能目录提示词段落。
// 传入的函数接收当前智能体过滤后的技能列表，
// 应返回完整的目录字符串（空字符串则省略该段落）。
// 为 nil 时（默认），使用内置的 buildSkillsCatalog。
func WithSkillsPrompt(builder func(skills []*skill.Skill) string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.skillsCatalogBuilder = builder }
}

// WithEnvs 覆盖系统提示词中默认的环境信息段落。
// 传入的函数接收 EnvsParams（SessionID、ProjectDir、SessionDir），
// 应返回完整的环境信息字符串（空字符串则省略）。
// 为 nil 时（默认），使用内置的 buildEnvironmentInfo。
func WithEnvs(builder func(params EnvsParams) string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.envsBuilder = builder }
}

// WithSearchStrategy 覆盖系统提示词中默认的搜索策略段落。
// 传入的函数无参数，应返回完整的段落字符串
//（空字符串则省略整个段落）。
// 为 nil 时（默认），使用内置的 buildSearchPriority。
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

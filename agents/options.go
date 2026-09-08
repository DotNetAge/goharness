package agents

import (
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
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

// WithProviderRegistry 设置大语言模型提供商配置注册表。
func WithProviderRegistry(reg config.ProviderRegistry) RuntimeConfig {
	return func(r *Runtime) { r.providerReg = reg }
}

// WithBaseSystemPrompt 注入应用侧（mindx）组装的基础系统提示词构造器。
//
// 这是提示词接缝的唯一入口：身份、技能目录、环境信息、搜索策略、Agent 公共规则、
// 用户/权限规则等应用语义段落全部由应用侧组装为一个完整的基础提示词；goharness
// 只在其后追加自己的机制段（行为准则、沟通风格）。
//
// builder 返回空字符串时跳过基础段（仅保留机制段，适用于 goharness 独立测试）。
func WithBaseSystemPrompt(builder func(sessionID string, s *session.Session) string) RuntimeConfig {
	return func(r *Runtime) { r.prompt.baseBuilder = builder }
}

// WithExcludeTools 注入按 Agent 名称解析排除工具集合的回调。
//
// goharness 对 Agent 结构零依赖：exclude_tools 属于应用侧的 Agent 属性，
// 应用在创建 Runtime 时注入解析函数，工具装配（主会话与子 Agent 会话共用）
// 按当前 agentName 取得需要排除的工具名集合。未注入时不排除任何工具。
func WithExcludeTools(resolver func(agentName string) []string) RuntimeConfig {
	return func(r *Runtime) { r.excludeTools = resolver }
}

// WithAgentExists 注入按名称校验 Agent 是否存在的回调。
//
// 子 Agent 派生（spawn）前用该回调校验目标 Agent 存在性；未注入时跳过校验。
func WithAgentExists(checker func(agentName string) bool) RuntimeConfig {
	return func(r *Runtime) { r.agentExists = checker }
}

func WithToolRegistry(reg tools.ToolRegistry) RuntimeConfig {
	return func(r *Runtime) { r.toolReg = reg }
}

// WithSkillRegistry 注入技能检索注册表（P4 SPI 收窄：goharness 只保留 GetSkill
// 检索契约，技能的发现/加载/注册由应用侧负责）。
// 未注入（nil）时不注册 Skill 工具。
func WithSkillRegistry(reg skill.SkillRegistry) RuntimeConfig {
	return func(r *Runtime) { r.prompt.skillReg = reg }
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
// （TaskCreate/TaskGet/TaskUpdate/TaskList）及其他 KV 感知工具
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
// 沙箱未设置（nil）时，所有工具拒绝执行（安全决策统一收口到沙箱，
// 工具自身不做任何授权检查；调用方必须通过本选项注入沙箱实例）。
func WithSandbox(sb *sandbox.Sandbox) RuntimeConfig {
	return func(r *Runtime) { r.sandbox = sb }
}

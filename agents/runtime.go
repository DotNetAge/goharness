// Package agents 提供执行 ReAct（推理 + 行动）循环的 AI Agent 运行时核心。
//
// 本包实现 AI 智能体的中央编排层，负责：
//   - 支持流式输出与工具调用的大语言模型交互
//   - 工具注册、发现及执行（同步 / 异步）
//   - 智能体能力目录（Skill）管理
//   - 行为规则与系统提示词构建
//   - 会话管理与对话历史压缩
//   - 扩展钩子系统（BeforeLLM、AfterLLM、Tool hooks）
//   - 事件发射，用于实时监控与可观测性
//
// # 架构概览
//
// Runtime 是替代旧 Reactor 架构的中央编排器，职责划分更清晰：
//
//	Runtime（本文件）
//	├── 模型配置（大语言模型设置）
//	├── 注册表（工具、技能、规则、智能体、提供商）
//	├── 执行器（工具执行引擎）
//	├── 钩子（LoopHooks + ToolHooks）
//	└── 日志器
//
//	Session（独立包）
//	├── 消息存储与读取
//	├── 对话窗口管理
//	├── 压缩（Token 限制下的滑动窗口）
//	└── 持久化
//
// # 思考循环生命周期
//
// 核心执行流程（exec 方法）实现 ReAct 循环：
//
//  1. 根据注册表和会话状态构建系统提示词
//  2. 执行 BeforeLLM hooks（可中止 / 修改）
//  3. 组装消息（system + 历史 + 用户问题）
//  4. 流式调用大语言模型（content + thinking + tool calls）
//  5. 执行 AfterLLM hooks（可中止 / 修改）
//  6. 若没有工具调用 → 返回最终答案
//  7. 若有工具调用 → 通过钩子链执行工具
//  8. 将结果追加到会话 → 回到步骤 1
//  9. 终止原因：completed / max_iterations / cancelled / error 等
//
// # 线程安全
//
// Runtime 设计为单个 Ask 上下文中单线程使用。
// 若需要并发调用 Ask，请创建独立的 Runtime 实例或在外部同步。
// 内部注册表通常在初始化后只读。
package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/hooks/action"
	"github.com/DotNetAge/goharness/hooks/loop"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/rule"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
	"github.com/DotNetAge/goharness/store"
	"github.com/DotNetAge/goharness/tools"
)

// 默认常量。
const (
	// defaultMaxIterations 是单轮 Ask 中思考循环的最大迭代次数。
	defaultMaxIterations = 20

	// defaultLLMTimeout 是单次大语言模型调用的默认超时。
	defaultLLMTimeout = 4 * time.Minute

	// duplicateErrorThreshold 是同一工具连续出现完全相同的错误时，
	// 触发「引导话术注入」的阈值。达到阈值后，我们不会硬性熔断循环，
	// 而是在工具结果中追加一段引导话术，提示大模型更换工作方式。
	duplicateErrorThreshold = 2

	// duplicateErrorGuideTemplate 是重复错误引导话术的模板。
	// %s 会被替换为工具名，%d 为连续相同错误的次数。
	// 采用第一人称自我反思格式，引导模型停下来思考目的与替代方案，
	// 而不是机械地继续用相同方式重试（见用户要求 3）。
	duplicateErrorGuideTemplate = "我连续 %d 次以完全相同的方式调用 %s 但均失败。\n" +
		"原因：当前做法无效，继续重复执行同样的指令只会浪费资源，也无法完成任务。\n" +
		"下一步我应该：\n" +
		"1. 停下来反思——我这样做的目的是什么？距离任务目标还差什么？\n" +
		"2. 思考是否还有其它方法可以更加有效地完成任务（更换参数、路径、工具或拆解步骤）；\n" +
		"3. 若确实无法通过该工具完成，应基于已有信息直接作答或询问用户，避免无意义的重试。"
)

// Runtime 承载运行思考循环所需的基础设施和注册表。
// 它替代旧的 Reactor：Runtime 与 Session 一起，在避免循环架构问题的同时复现 Reactor 的全部能力。
//
// # 字段说明
//
//   - model: 大语言模型配置，包括 API 密钥、基础 URL、模型名、温度、最大 token 数及采样参数等。
//   - toolReg: 可用工具注册表（Grep、Bash、WebSearch 等）。
//   - mem: 向量存储与检索（RAG）接口。
//   - providerReg: 大语言模型提供商配置注册表。
//   - prompt: 提示词装配器，持有 agent/skill/rule 注册表与可覆盖段落构造器。
//   - toolExec: 工具执行引擎，支持钩子与事件发射。
//   - logger: 结构化日志器。
//   - loopHooks: 思考循环中每次大语言模型调用前后运行的钩子。
//   - toolHooks: 每次工具执行前后运行的钩子。
//   - asyncTimeout: 异步（并发）工具的最大执行时间。
//   - syncTimeout: 同步（顺序）工具的最大执行时间。
type Runtime struct {
	model config.ModelConfig

	toolReg tools.ToolRegistry
	mem     memory.Memory

	providerReg config.ProviderRegistry
	toolExec    tools.ToolExecutor

	// prompt 承载系统提示词与消息序列的构造职责（持有 agent/skill/rule 注册表引用
	// 与可覆盖的段落构造器）。通过 WithAgentRegistry/WithSkillRegistry/WithRuleRegistry
	// 及 WithSkillsPrompt/WithEnvs/WithSearchStrategy 配置。
	prompt PromptAssembler

	// kvStore 为需要会话级持久化的工具（TaskCreate / TaskGet / TaskUpdate / TaskList）
	// 提供键值存储。若为 nil，这些工具返回“KVStore 不可用”。通过 WithKVStore 配置。
	kvStore store.KVStore

	// sessionStore 用于加载子会话消息以支持 CollectResults 恢复。通过 WithSessionStore 配置。
	sessionStore session.SessionStore

	logger    logging.Logger
	loopHooks []hooks.LoopHook
	toolHooks []hooks.ToolHook

	asyncTimeout time.Duration
	syncTimeout  time.Duration

	// subAgents 管理子智能体的会话登记与派生执行（详见 subAgentManager）。
	// 以 SessionID 为唯一键定位会话：不传 ID 时每次新建（分身/并行委派），
	// 传 ID 时复用旧会话（延续对话）。
	subAgents *subAgentManager

	// tokenUsageStore 持久化大语言模型 token 使用记录，包含分组维度与成本。
	// 默认：NoopTokenUsageStore（空操作）。通过 WithTokenUsageStore 注入。
	tokenUsageStore session.TokenUsageStore

	// fileModifyTracker 为 FileModifyHook 提供按 sessionID 的 TrackFunc。
	// 通过 WithFileModifyTracker 设置，以在 Write / FileEdit 前自动备份文件。
	fileModifyTracker action.TrackerProvider

	// fileModifyHook 保存已注册 FileModifyHook 的引用，
	// 使 WithFileModifyTracker 能在初始化后动态更新其 provider。
	fileModifyHook *action.FileModifyHook

	// llmClient 负责与大语言模型交互。
	// 默认为基于 gochat 的实现；可通过 WithLLMClient 注入 mock 或其他提供商实现。
	llmClient LLMClient

	// sandbox 是会话级逻辑沙箱实例。
	// 通过 WithSandbox 注入；为 nil 时所有工具回退到旧安全逻辑。
	// 自动注入到 Runtime 创建的所有子 Agent 会话；主会话由调用方通过 rt.Sandbox() 获取并注入。
	sandbox *sandbox.Sandbox
}

// RunResult 保存单次 Ask 调用的执行结果，
// 包括最终答案、资源使用情况及终止信息。
type RunResult struct {
	Answer            string
	TokenUsage        session.TokenUsage
	Duration          time.Duration
	Iterations        int
	TerminationReason string
}

// NewRuntime 使用给定的配置选项创建新的 Runtime 实例。
// 自动初始化所有默认注册表、工具，并设置合理的超时默认值。
//
// 默认行为：
//   - 默认工具注册表，包含 15+ 内置工具（Grep、Glob、Read、Write、Bash 等）
//   - 默认技能注册表（空，可继续注册）
//   - 默认日志器（标准输出，结构化 JSON）
//   - 同步 / 异步工具超时均为 5 分钟
//   - 无智能体注册表、规则注册表或记忆（nil）
//   - 未注册任何钩子
//
// 参数 opts 是可变 RuntimeConfig 函数列表，常见选项包括：
//   - WithModel(config.ModelConfig)：设置大语言模型配置（必需）
//   - WithToolRegistry(tools.ToolRegistry)：使用自定义工具注册表
//   - WithSkillRegistry(skill.SkillRegistry)：使用自定义技能注册表
//   - WithRuleRegistry(rule.RuleRegistry)：使用自定义规则注册表
//   - WithAgentRegistry(*config.AgentRegistry)：使用自定义智能体注册表
//   - WithProviderRegistry(config.ProviderRegistry)：使用自定义提供商注册表
//   - WithMemory(memory.Memory)：设置记忆 / RAG 后端
//   - WithLogger(logging.Logger)：设置自定义日志器
//   - WithLoopHooks(...hooks.LoopHook)：添加循环生命周期钩子
//   - WithToolHooks(...hooks.ToolHook)：添加工具执行钩子
//   - WithAsyncTimeout(time.Duration)：设置异步工具超时（默认 5 分钟）
//   - WithSyncTimeout(time.Duration)：设置同步工具超时（默认 5 分钟）
func NewRuntime(opts ...RuntimeConfig) *Runtime {
	r := &Runtime{
		toolReg:      tools.NewDefaultToolRegistry(),
		logger:       logging.DefaultLogger(),
		asyncTimeout: 5 * time.Minute,
		syncTimeout:  5 * time.Minute,
		prompt: PromptAssembler{
			skillReg: skill.NewDefaultSkillRegistry(),
		},
	}
	r.subAgents = newSubAgentManager(r)
	for _, opt := range opts {
		opt(r)
	}
	// logger 保障：若 WithLogger(nil) 被调用，回退到默认日志器。
	// 策略统一为：NewRuntime 保证 logger 非 nil，函数体内一律不做 nil 检查。
	if r.logger == nil {
		r.logger = logging.DefaultLogger()
	}
	if r.tokenUsageStore == nil {
		r.tokenUsageStore = session.NewNoopTokenUsageStore()
	}
	if r.llmClient == nil {
		r.llmClient = NewDefaultLLMClient(r.model.APIKey, r.model.BaseURL, r.model.Provider)
	}
	r.toolExec = tools.NewToolExecutor(r.toolReg,
		tools.WithSessionStore(r.sessionStore),
		tools.WithKVStore(r.kvStore),
	)
	r.registerDefaultTools()
	r.registerDefaultHooks()
	return r
}

// toolFactory 描述一个工具注册项：名称 + 构造函数。
type toolFactory struct {
	name    string
	factory func() tools.FuncTool
}

// toolOf 将返回具体工具类型的构造函数包装为统一的 FuncTool 工厂，
// 避免重复书写结构体字面量与类型断言。
func toolOf[T tools.FuncTool](name string, new func() T) toolFactory {
	return toolFactory{name: name, factory: func() tools.FuncTool { return new() }}
}

// registerDefaultTools 将所有内置工具注册到工具注册表。
// 由 NewRuntime 在初始化期间自动调用。
// 包含文件操作（Grep、Glob、Read、Write、FileEdit、Ls）、执行工具（Bash、RunScript）、
// 网络工具（WebSearch、WebFetch）、交互工具（AskUser）及任务管理工具。
// 在 Windows 上额外注册 PowerShell 工具。
// 当模型为本地部署（IsLocal=true）时，跳过 SubAgent 和 TeamXXX 等多 Agent 工具的注册，
// 因为本地模型通常无法可靠地执行多 Agent 并行任务。
func (rt *Runtime) registerDefaultTools() {
	// 图片读取仅对支持视觉理解的模型（Visioning=true）生效：
	// 图片读取会返回 base64 数据，对不支持视觉的模型没有意义且浪费上下文。
	readLimits := tools.DefaultFileReadingLimits()
	if rt.model.Visioning {
		readLimits.EnableImageReading = true
	}

	bundled := []toolFactory{
		toolOf("Grep", tools.NewGrepTool),
		toolOf("Glob", tools.NewGlobTool),
		toolOf("Read", func() *tools.Read { return tools.NewReadToolWithLimits(readLimits) }),
		toolOf("Write", tools.NewWriteTool),
		toolOf("Edit", tools.NewEditTool),
		toolOf("Bash", tools.NewBashTool),
		toolOf("RunScript", tools.NewRunScriptTool),
		toolOf("WebSearch", func() tools.FuncTool { return tools.NewWebSearchTool(rt.logger) }),
		toolOf("WebFetch", func() tools.FuncTool { return tools.NewWebFetchTool(rt.logger) }),
		toolOf("AskUser", tools.NewAskUserTool),
		toolOf("Ls", tools.NewLsTool),
	}

	// 本地模型不注册多 Agent / 任务管理类工具：
	// 1. SubAgent 与 CollectResults 是配对的——不注册 SubAgent 就不注册 CollectResults
	// 2. Task 系列（TaskCreate/TaskList/TaskGet/TaskUpdate）依赖多 Agent 协作
	// 3. Sleep/Skill 与任务编排相关，本地模型场景一并排除
	// 因为本地模型通常无法可靠地执行多 Agent 并行任务
	if !rt.model.IsLocal {
		bundled = append(bundled,
			toolOf("CollectResults", tools.NewCollectResultsTool),
			toolOf("TaskCreate", tools.NewTaskCreateTool),
			toolOf("TaskList", tools.NewTaskListTool),
			toolOf("TaskGet", tools.NewTaskGetTool),
			toolOf("TaskUpdate", tools.NewTaskUpdateTool),
			// toolOf("Sleep", tools.NewSleepTool),
			toolOf("Skill", func() *tools.SkillTool { return tools.NewSkillTool(rt.prompt.skillReg.GetSkill) }),
			toolOf("SubAgent", func() *tools.SubAgentTool {
				subAgentTool := tools.NewSubAgentTool(rt.subAgents.spawn)
				subAgentTool.SetEnsureSessionFunc(func(ctx context.Context, agentName, sessionID string) (string, error) {
					tc := tools.GetToolContext(ctx)
					if tc == nil || tc.Session == nil {
						return "", fmt.Errorf("上下文未包含会话")
					}
					st, err := rt.subAgents.getOrCreate(ctx, agentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store(), sessionID)
					if err != nil {
						return "", err
					}
					return st.sess.ID(), nil
				})
				return subAgentTool
			}),
			toolOf("TeamCreate", func() *tools.TeamCreateTool { return tools.NewTeamCreateTool(rt.subAgents.spawn) }),
			toolOf("TeamDelete", tools.NewTeamDeleteTool),
			toolOf("TeamList", tools.NewTeamListTool),
			toolOf("TeamGetTasks", tools.NewTeamGetTasksTool),
		)
	}

	for _, b := range bundled {
		if t := b.factory(); t != nil {
			_ = rt.toolReg.Register(t)
		}
	}
	if tools.IsWindowsPlatform() {
		if ps := tools.NewPowerShellTool(); ps != nil {
			_ = rt.toolReg.Register(ps)
		}
	}
}

// registerDefaultHooks 注册默认的循环钩子与工具钩子。
// 由 NewRuntime 在初始化期间自动调用。
//
// 循环钩子（每轮按优先级顺序执行）：
//   - LoopLoggerHook (45)：大语言模型调用日志
//   - ConvergenceHook (49)：不可恢复错误检测 → 中止
//   - MemoryThoughtHook (50)：RAG 上下文注入（设置 mem 时前置）
//
// 工具钩子（每次工具调用按优先级顺序执行）：
//   - PermissionHook (41)：权限链评估 → 拒绝并中止
//   - FileModifyHook (42)：Write / FileEdit 前备份文件（provider 可能为 nil）
//   - ToolLoggerHook (46)：工具执行日志
func (rt *Runtime) registerDefaultHooks() {
	rt.loopHooks = append(rt.loopHooks, loop.Defaults(rt.logger)...)
	if rt.mem != nil {
		hook := loop.NewMemoryThoughtHook(rt.mem)
		hook.Logger = rt.logger
		rt.loopHooks = append([]hooks.LoopHook{hook}, rt.loopHooks...)
	}

	// 注册默认工具钩子
	defaultHooks := action.Defaults(nil, rt.prompt.skillReg, rt.logger, rt.fileModifyTracker)

	// 捕获 FileModifyHook 引用，以便通过 WithFileModifyTracker 延迟绑定。
	rt.fileModifyHook = nil
	for _, h := range defaultHooks {
		if fmh, ok := h.(*action.FileModifyHook); ok {
			rt.fileModifyHook = fmh
			break
		}
	}

	rt.toolHooks = append(rt.toolHooks, defaultHooks...)

	// 图片提取 Hook：仅对支持视觉理解的模型（Visioning=true）注册。
	// 图片读取开关（EnableImageReading）控制 Read 是否返回图片数据；
	// 本 Hook 负责把图片转换为 image_url 消息注入上下文。二者配合，
	// 非视觉模型既不读取图片也不注入图片，避免浪费上下文。
	if rt.model.Visioning {
		rt.toolHooks = append(rt.toolHooks, action.NewImageHook(rt.logger))
	}
}

// SessionConfigs 返回 Runtime 应注入到所有会话（主会话与子会话）的通用 SessionConfig 列表。
//
// 这里注入的是 Runtime 提供的通用能力，所有会话共享：
//   - Compactor：上下文压缩引擎。其依赖（llmClient/model/请求构造路径）全部来自
//     Runtime，是 goharness 内置能力，无需外部应用注入——外部只需在创建会话时
//     合并本方法返回的配置即可启用压缩。
//   - Sandbox：工具安全沙箱（若已配置）。
//
// 会话特有的配置（如 MemoryStore、ModelContextResolver，依赖会话级数据）由调用方
// 在创建会话时额外追加，不应放入此处。
//
// 注意：本方法是「Runtime 通用会话配置提供者」，并非子智能体专属——主会话同样使用
// （见 mindx app.go / daemon.go），故保留在 Runtime 而非 subAgentManager 上。
func (rt *Runtime) SessionConfigs() []session.SessionConfig {
	opts := []session.SessionConfig{
		session.WithCompactor(NewCompactor(rt)),
	}
	if rt.sandbox != nil {
		opts = append(opts, session.WithSandbox(rt.sandbox))
	}
	return opts
}

// Ask 创建用于启动与智能体对话的 AskBuilder。
// 这是与 Runtime 思考循环交互的主要入口。
//
// Ask 使用构建器模式，允许在执行前灵活配置。
// 返回的 AskBuilder 可通过事件处理器定制，然后执行以运行完整的 ReAct 循环。
//
// 参数：
//   - agentName: 要使用的智能体配置标识符。必须匹配 AgentRegistry 中注册的名称。
//     智能体配置定义系统提示词中使用的角色、描述和介绍。
//   - question: 用户的问题或指令，将作为用户消息追加到会话并发送给大语言模型。
//   - s: 维护对话历史和状态的 Session 实例。每次 Ask 调用都会向该会话追加消息。
//
// 返回 *AskBuilder，可调用 builder.Execute() 运行思考循环并获取结果，
// 或调用 builder.OnEvent() 注册实时更新的事件处理器。
func (rt *Runtime) Ask(agentName, question string, s *session.Session) *AskBuilder {
	ctx, cancel := context.WithCancel(context.Background())
	return &AskBuilder{
		ctx:       ctx,
		cancel:    cancel,
		agentName: agentName,
		question:  question,
		session:   s,
		runtime:   rt,
		onEvent:   make(map[events.ReactEventType][]func(any)),
	}
}

// Logger 返回 Runtime 的结构化日志器实例。
// 日志器用于运行时中的调试、信息、警告和错误消息。
// 默认实现将 JSON 格式日志输出到标准输出。
func (rt *Runtime) Logger() logging.Logger { return rt.logger }

// Sandbox 返回 Runtime 持有的会话级逻辑沙箱实例。
// 返回 nil 表示沙箱未启用，所有工具回退到旧安全逻辑。
// 调用方在创建主会话时应通过此方法获取沙箱并注入：
//
//	sess, _ := session.New(..., session.WithSandbox(rt.Sandbox()))
//
// 子 Agent 会话由 Runtime 自动注入，无需调用方处理。
func (rt *Runtime) Sandbox() *sandbox.Sandbox { return rt.sandbox }

// ToolRegistry 返回 Runtime 的工具注册表，其中包含所有已注册工具。
// 工具注册表管理大语言模型在执行期间可调用的可用工具。
// NewRuntime 自动注册内置工具；额外工具可通过 RegisterTool 或 WithToolRegistry 注册。
func (rt *Runtime) ToolRegistry() tools.ToolRegistry { return rt.toolReg }

// SkillRegistry 返回 Runtime 的技能注册表，用于管理智能体能力。
// 技能定义在系统提示词中向智能体展示的高级能力。
// 与工具（函数调用）不同，技能描述智能体能做什么。
func (rt *Runtime) SkillRegistry() skill.SkillRegistry { return rt.prompt.skillReg }

// RuleRegistry 返回 Runtime 的行为规则注册表。
// 规则定义智能体应如何表现、应避免什么以及任何操作边界。规则会纳入系统提示词。
func (rt *Runtime) RuleRegistry() rule.RuleRegistry { return rt.prompt.ruleReg }

// ProviderRegistry 返回 Runtime 的大语言模型提供商注册表。
// 提供商配置可用于多提供商设置或回退逻辑。
func (rt *Runtime) ProviderRegistry() config.ProviderRegistry { return rt.providerReg }

// ToolExecutor 返回 Runtime 的工具执行引擎。
// 执行器处理带有钩子支持、超时管理和事件发射的工具调用。
// 在自定义代码中执行工具时，建议使用此执行器而非直接调用工具，
// 因为它包含钩子链和错误处理。
func (rt *Runtime) ToolExecutor() tools.ToolExecutor {
	return rt.toolExec
}

// AgentRegistry 返回 Runtime 的智能体配置注册表。
// 智能体配置定义角色、描述和介绍，用于构建系统提示词中的身份段落。
func (rt *Runtime) AgentRegistry() *config.AgentRegistry { return rt.prompt.agentReg }

// WithFileModifyTracker 设置当前 Runtime 的文件修改追踪器 provider。
// 设置后，默认工具钩子中会自动注册 FileModifyHook，以在 Write / FileEdit 工具执行前备份文件。
//
// provider 函数接收 sessionID，返回该会话的 TrackModify 函数（若该会话无追踪能力则返回 false）。
func (rt *Runtime) WithFileModifyTracker(provider action.TrackerProvider) {
	rt.fileModifyTracker = provider
	if rt.fileModifyHook != nil {
		rt.fileModifyHook.SetProvider(provider)
	}
}

// RegisterTool 向 Runtime 的工具注册表添加新工具。
// 该工具将在后续 Ask 调用中可供大语言模型调用。
// 工具定义会作为 API 请求的一部分发送给大语言模型。
//
// 参数 tool 必须实现 FuncTool，且 Info() 中具有有效的 Name、Description 和 Parameters。
// 返回 error：当工具注册失败时非 nil（例如重名或无效配置）。
func (rt *Runtime) RegisterTool(tool tools.FuncTool) error {
	return rt.toolReg.Register(tool)
}

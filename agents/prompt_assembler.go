package agents

import (
	"strings"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/rule"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
)

// defaultCompactWindowThreshold 是 MicroCompact 启用区间的下界（128K）。
// 低于此值的模型由 TryCompact 独占管理（80% 触发全量摘要清空），
// 不需要 MicroCompact 的局部压缩。
const defaultCompactWindowThreshold = 128 * 1024

// microCompactMaxContextThreshold 是 MicroCompact 启用区间的上界（250K）。
// 仅当 128K < ModelContextLength <= 250K 时调用 TryMicroCompact：
//   - ≤128K：TryCompact 独占管理，无需 MicroCompact
//   - 128K–250K：MicroCompact（45% 触发局部压缩）先于 TryCompact（80% 触发全量清空）执行
//   - >250K：不启用，避免修改上下文中间 tool 消息导致 KV 缓存重算成本过高
const microCompactMaxContextThreshold = 250 * 1024

// shouldEnableMicroCompact 判断当前模型上下文长度是否应启用 MicroCompact。
// 启用区间：128K < ContextLength <= 250K。
//   - ≤128K：由 TryCompact 独占管理（80% 触发全量摘要清空），无需 MicroCompact
//   - 128K–250K：MicroCompact（45% 触发局部压缩）先于 TryCompact（80% 触发全量清空）执行
//   - >250K：不启用，避免修改上下文中间 tool 消息导致 KV 缓存重算成本过高
//
// 该条件在 executor.go（控制 TryMicroCompact 调用）与 prompt_assembler.go
// （控制压缩内容占位符插入）两处共用，集中此处避免修改时遗漏其一。
func shouldEnableMicroCompact(ctxLen int64) bool {
	return ctxLen > defaultCompactWindowThreshold && ctxLen <= microCompactMaxContextThreshold
}

// PromptAssembler 负责构造发送给 LLM 的系统提示词与消息序列。
// 它从 Runtime 抽离提示词构造职责，集中持有相关注册表引用与可覆盖的段落构造器，
// 使 Runtime 退回装配根，提示词逻辑可独立测试与演进。
type PromptAssembler struct {
	agentReg *config.AgentRegistry
	skillReg skill.SkillRegistry
	ruleReg  rule.RuleRegistry

	// 以下三个构造器为 nil 时回退到内置默认实现
	// （buildSkillsCatalog / buildEnvironmentInfo / buildSearchPriority）。
	skillsCatalogBuilder  func(skills []*skill.Skill) string
	envsBuilder           func(EnvsParams) string
	searchStrategyBuilder func() string
}

// BuildSystemPrompts 根据注册表和会话状态构造系统提示词。
//
// 系统提示词拆分为多个段落，以实现：
//   - 关注点分离（身份、技能、规则、环境等）
//   - KV 缓存优化（静态与动态边界）
//   - Hook 可以选择性修改特定段落
//
// 段落顺序：
//  1. 身份 — 来自 AgentRegistry 的智能体名称、角色、描述、介绍
//  2. 技能目录 — 仅包含当前智能体声明的技能
//  3. 行为规则 — 默认规则 + 自定义规则
//  4. 搜索优先级 — 本地搜索与网络搜索的优先级说明
//  5. 环境信息 — 会话 ID、工作目录等
//  6. 压缩内容占位符 — 仅在 MicroCompact 启用区间（128K < ContextLength <= 250K）时插入
//  7. 输出效率 — 简洁输出相关指令
//
// 最终合并为单条 system 消息，以集中大模型对系统规则的注意力。
func (p *PromptAssembler) BuildSystemPrompts(sessionID string, s *session.Session) []gochatcore.Message {
	var sections []string

	// 预取智能体配置（身份与技能目录两段共用，避免重复查询注册表）。
	var agentCfg *config.AgentConfig
	if p.agentReg != nil {
		agentCfg = p.agentReg.Get(s.AgentName())
	}

	// 1. 身份
	if agentCfg != nil {
		sections = append(sections,
			buildIdentity(agentCfg.Name, agentCfg.Role, agentCfg.Description, agentCfg.Introduction))
	}

	// 2. 技能目录 — 仅包含当前智能体声明的技能
	if p.skillReg != nil && agentCfg != nil && len(agentCfg.Skills) > 0 {
		allSkills := p.skillReg.ListSkills()
		allowed := make(map[string]bool, len(agentCfg.Skills))
		for _, name := range agentCfg.Skills {
			allowed[name] = true
		}
		var agentSkills []*skill.Skill
		for _, sk := range allSkills {
			if allowed[sk.Name] {
				agentSkills = append(agentSkills, sk)
			}
		}
		if catalog := p.skillsCatalog(agentSkills); catalog != "" {
			sections = append(sections, catalog)
		}
	}

	// 3. 行为规则（默认规则 + 可选的自定义扩展规则）
	sections = append(sections, defaultBehavioralRules())
	if p.ruleReg != nil {
		if custom := p.ruleReg.FormatPromptSection(); custom != "" {
			sections = append(sections, "## 扩展规则\n\n"+custom)
		}
	}

	// 4. 搜索优先级
	sections = append(sections, p.buildSearchStrategy())

	// 5. 环境信息
	sections = append(sections, p.buildEnvs(EnvsParams{
		SessionID:  sessionID,
		SessionDir: s.SessionDir(),
		ProjectDir: s.ProjectDir(),
	}))

	// 6. 压缩内容占位符：仅在 MicroCompact 启用区间（128K < ContextLength <= 250K）时插入。
	//    该占位符向 LLM 解释 [已压缩] 标记的格式和规则，
	//    必须与 executor.go 中的 TryMicroCompact 调用保持同步。
	if shouldEnableMicroCompact(s.ModelContextLength()) {
		sections = append(sections, buildCompressedContent())
	}

	// 7. 输出效率
	sections = append(sections, buildOutputEfficiency())

	return []gochatcore.Message{gochatcore.NewSystemMessage(strings.Join(sections, "\n\n"))}
}

// skillsCatalog 构造技能目录段落。如果提供了覆盖构造器则使用它，
// 否则回退到默认的 buildSkillsCatalog。
func (p *PromptAssembler) skillsCatalog(agentSkills []*skill.Skill) string {
	if p.skillsCatalogBuilder != nil {
		return p.skillsCatalogBuilder(agentSkills)
	}
	return buildSkillsCatalog(agentSkills)
}

// buildEnvs 构造环境信息段落。如果提供了覆盖构造器则使用它，
// 否则回退到默认的 buildEnvironmentInfo。
func (p *PromptAssembler) buildEnvs(params EnvsParams) string {
	if p.envsBuilder != nil {
		return p.envsBuilder(params)
	}
	return buildEnvironmentInfo(params)
}

// buildSearchStrategy 构造搜索策略段落。如果提供了覆盖构造器则使用它，
// 否则回退到默认的 buildSearchPriority。
func (p *PromptAssembler) buildSearchStrategy() string {
	if p.searchStrategyBuilder != nil {
		return p.searchStrategyBuilder()
	}
	return buildSearchPriority()
}

// AssembleMessages 构造发送给 LLM API 的完整消息序列。
// 组合系统提示词、对话历史以及当前用户问题，并保持 LLM 提供商期望的消息顺序。
// 该函数为纯函数：不依赖任何接收者状态，便于独立测试与复用。
//
// 消息顺序：
//  1. 系统提示词段落
//  2. 对话历史（最多允许两条连续同角色消息）
//  3. 当前用户问题（若历史末尾不是同内容的问题）
func AssembleMessages(systemSections []gochatcore.Message, history []session.Message, question string) []gochatcore.Message {
	var msgs []gochatcore.Message
	msgs = append(msgs, systemSections...)

	// 过滤掉没有对应 tool 响应的孤立 tool_call。
	// 这可以避免思考循环中途取消后，助手的 tool_call 消息已持久化但工具结果未写入，
	// 导致下一次 LLM 请求因严格校验而失败。
	window := stripOrphanedToolCalls(history)

	// 构建 tool_call_id -> tool_name 映射，用于渲染压缩占位符
	toolNameByID := session.BuildToolNameByID(history)
	for _, m := range window {
		switch m.Role {
		case "system":
			msgs = append(msgs, gochatcore.NewSystemMessage(m.Content))
		case "user":
			// 携带图片的用户消息 → 组装为多模态消息（文本 + image_url 内容块）。
			// 图片以 image_url 形式进入上下文，与文本内容分离。
			if len(m.Images) > 0 {
				msg := gochatcore.Message{Role: "user"}
				if m.Content != "" {
					msg.Content = append(msg.Content, gochatcore.ContentBlock{
						Type: gochatcore.ContentTypeText, Text: m.Content,
					})
				}
				for _, img := range m.Images {
					msg.Content = append(msg.Content, gochatcore.ContentBlock{
						Type:      gochatcore.ContentTypeImage,
						MediaType: img.MediaType,
						Data:      img.Base64Data,
					})
				}
				msgs = append(msgs, msg)
			} else {
				msgs = append(msgs, gochatcore.NewUserMessage(m.Content))
			}
		case "assistant":
			msg := gochatcore.NewTextMessage("assistant", m.Content)
			msg.ReasoningContent = m.ReasoningContent
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, gochatcore.ToolCall{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			msgs = append(msgs, msg)
		case "tool":
			// 当内容被归档时渲染压缩占位符
			content := m.Content
			if m.Compacted != "" {
				content = session.RenderCompactedPlaceholder(m, toolNameByID)
			}
			toolMsg := gochatcore.NewTextMessage("tool", content)
			toolMsg.ToolCallID = m.ToolCallID
			msgs = append(msgs, toolMsg)
		default:
			msgs = append(msgs, gochatcore.NewTextMessage(m.Role, m.Content))
		}
	}

	// 追加当前用户问题（如果历史末尾不是同一内容的问题）
	if question != "" {
		if len(window) == 0 || window[len(window)-1].Role != "user" || window[len(window)-1].Content != question {
			msgs = append(msgs, gochatcore.NewUserMessage(question))
		}
	}

	return msgs
}

// AgentExcludeTools 返回指定 Agent 配置中声明要排除的工具集合。
// 若 Agent 注册表不可用或未找到该 Agent，返回空集合（不排除任何工具）。
func (p *PromptAssembler) AgentExcludeTools(agentName string) map[string]bool {
	excluded := make(map[string]bool)
	if p.agentReg == nil {
		return excluded
	}
	if cfg := p.agentReg.Get(agentName); cfg != nil {
		for _, name := range cfg.ExcludeTools {
			excluded[name] = true
		}
	}
	return excluded
}

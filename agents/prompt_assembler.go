package agents

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/skill"
)

// PromptAssembler 负责构造发送给 LLM 的系统提示词与消息序列。
// 它从 Runtime 抽离提示词构造职责，集中持有相关注册表引用，
// 使 Runtime 退回装配根，提示词逻辑可独立测试与演进。
//
// 职责切分（PR-PROMPTS）：全部应用语义段落（身份、SOUL、技能目录、AGENTS.md
// 公共规则、环境、搜索策略、用户/权限规则）由应用侧通过 WithBaseSystemPrompt
// 注入的 baseBuilder 组装；goharness 不生成、不追加任何文案段。
type PromptAssembler struct {
	skillReg skill.SkillRegistry

	// baseBuilder 为应用侧注入的基础系统提示词构造器。
	// 为 nil 或返回空字符串时跳过基础段（适用于 goharness 独立测试）。
	baseBuilder func(sessionID string, s *session.Session) string
}

// BuildSystemPrompts 根据会话状态构造系统提示词。
//
// 段落顺序（静态在前、动态在后，保证 KV 缓存前缀稳定）：
// 输出即应用侧 baseBuilder 返回的单条 system 消息。goharness 不追加任何
// 文案段（原机制段「行为准则/沟通风格」经 P4 评审认定属于应用语义，
// 已迁至应用侧 AGENTS.md 内嵌位）；memory 摘要经 Hook 在请求时机动态追加。
func (p *PromptAssembler) BuildSystemPrompts(sessionID string, s *session.Session) []gochatcore.Message {
	var sections []string

	// 基础提示词（应用语义，由应用侧组装）
	if p.baseBuilder != nil {
		if base := p.baseBuilder(sessionID, s); base != "" {
			sections = append(sections, base)
		}
	}

	// 合并为单条 system 消息，以集中大模型对系统规则的注意力。
	// base 为空时返回空 system 消息（保持消息结构稳定，供 Hook 定位锚点）。
	return []gochatcore.Message{
		gochatcore.NewSystemMessage(strings.Join(sections, "\n\n")),
	}
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
					block, ok := resolveImageBlock(img)
					if !ok {
						// 图片文件缺失或为空引用：降级为文本占位，不阻断对话。
						msg.Content = append(msg.Content, gochatcore.ContentBlock{
							Type: gochatcore.ContentTypeText,
							Text: fmt.Sprintf("[图片已失效: %s]", img.Path),
						})
						continue
					}
					msg.Content = append(msg.Content, block)
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
			toolMsg := gochatcore.NewTextMessage("tool", m.Content)
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

// resolveImageBlock 将持久化的图片块解析为 gochat 内容块。
// 优先使用内联 base64 数据（工具结果图片场景）；
// Path 引用场景在组装时才读文件转 base64，使持久化消息只存路径引用。
// 读取失败或引用为空时返回 false，由调用方降级处理。
func resolveImageBlock(img session.ImageBlock) (gochatcore.ContentBlock, bool) {
	data := img.Base64Data
	if data == "" && img.Path != "" {
		raw, err := os.ReadFile(img.Path)
		if err != nil || len(raw) == 0 {
			return gochatcore.ContentBlock{}, false
		}
		data = base64.StdEncoding.EncodeToString(raw)
	}
	if data == "" {
		return gochatcore.ContentBlock{}, false
	}
	mediaType := img.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return gochatcore.ContentBlock{
		Type:      gochatcore.ContentTypeImage,
		MediaType: mediaType,
		Data:      data,
	}, true
}

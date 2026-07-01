package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goharness/events"
)

// AskUser 实现了非阻塞用户交互工具。
// 与旧设计的区别：
//   - 非阻塞设计：工具直接返回，不等待用户输入
//   - SecurityLevel 为 LevelSafe，不触发权限检查
//   - 思考循环在工具执行后被 runtime 检测并暂停
//   - 用户的回答作为普通用户消息发送给 LLM
//
// 交互流程：
//  1. LLM 调用 AskUser 工具并传入问题
//  2. 工具直接返回提示消息
//  3. Runtime 检测到 AskUser 调用，发射 AskUserPending 事件
//  4. 思考循环暂停
//  5. 前端显示问题对话框
//  6. 用户选择答案后通过普通消息发送
//  7. LLM 从新对话周期继续推理
type AskUser struct {
	info *ToolInfo
}

// NewAskUserTool 创建一个 AskUser 工具实例。
//
// 返回：
//   - FuncTool: 配置好的 AskUser 工具实例
func NewAskUserTool() FuncTool {
	return &AskUser{
		info: &ToolInfo{
			Name:        "AskUser",
			Description: "Ask the user a question to gather information, clarify ambiguity, or make decisions.",
			Prompt: `Ask the user a question during execution. Use this to gather info, clarify ambiguity, or get decisions.

Users can always type a custom answer via "Other". Use multiSelect: true for non-exclusive choices.`,
			Tags:          []string{"interaction", "question", "clarify", "human"},
			IsReadOnly:    false,
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{
					Name:        "question",
					Type:        "string",
					Description: "The clarifying question to ask the user. Be specific and concise.",
					Required:    true,
				},
				{
					Name:        "options",
					Type:        "array",
					Description: "Optional list of answer choices (2-4 items). If you recommend a specific option, add \"(Recommended)\" at the end of its label. Omit for open-ended questions.",
					Required:    false,
				},
				{
					Name:        "multiSelect",
					Type:        "boolean",
					Description: "Set to true to allow the user to select multiple options instead of just one. Use when choices are not mutually exclusive.",
					Required:    false,
				},
			},
		},
	}
}

// Info 返回 AskUser 工具的元信息。
func (t *AskUser) Info() *ToolInfo {
	return t.info
}

// Execute 执行非阻塞用户提问操作。
// 直接返回提示消息，告诉 LLM 问题已提出，等待用户响应。
// Runtime 会检测 AskUser 调用并暂停思考循环。
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "question"，可选 "options" 和 "multiSelect"
//
// 返回：
//   - string: 提示消息
//   - error: 缺少必需参数时返回错误
func (t *AskUser) Execute(ctx context.Context, params map[string]any) (any, error) {
	question, ok := params["question"].(string)
	if !ok || question == "" {
		return nil, fmt.Errorf("missing required parameter: question")
	}

	return fmt.Sprintf(`Asked user: "%s". Waiting for their response...`, question), nil
}

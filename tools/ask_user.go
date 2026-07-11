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
			Description: "向用户提问以收集信息、澄清歧义或做出决策。",
			Prompt: `在执行过程中向用户提问。用于收集信息、澄清歧义或获取决策。

用户始终可以通过"其他"输入自定义答案。对于非互斥选项，请使用 multiSelect: true。`,
			Tags:          []string{"interaction", "question", "clarify", "human"},
			IsReadOnly:    false,
			SecurityLevel: events.LevelSafe,
			Parameters: []Parameter{
				{
					Name:        "question",
					Type:        "string",
					Description: "要向用户提出的澄清性问题。请具体且简洁。",
					Required:    true,
				},
				{
					Name:        "options",
					Type:        "array",
					Description: "可选的答案选项列表（2-4 项）。如果您推荐特定选项，请在其标签末尾添加\"（推荐）\"。开放式问题可省略。",
					Required:    false,
				},
				{
					Name:        "multiSelect",
					Type:        "boolean",
					Description: "设置为 true 以允许用户选择多个选项而不是仅选择一个。当选项不是互斥时使用。",
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
		return nil, fmt.Errorf("缺少必需参数：question")
	}

	return fmt.Sprintf(`已向用户提问："%s"。等待他们的回复...`, question), nil
}

package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DotNetAge/goreact/events"
)

// AskUser 实现了用户交互工具。
// 用于在执行过程中向用户提问，收集信息、澄清歧义或获取决策。
//
// 与旧设计的区别：
//   - 不再返回 _interaction marker
//   - 通过 AskUserRequest 事件发送结构化问题
//   - 权限对话框收集答案，通过 UpdatedInput 注入参数
//   - Execute() 是恒等函数，将答案格式化为自然语言消息
//
// 交互流程：
//  1. LLM 调用 AskUser 工具并传入问题
//  2. 执行器检测到 AskUser，发送 AskUserRequest 事件
//  3. 前端显示问题对话框
//  4. 用户选择或输入答案
//  5. 答案通过 UpdatedInput 注入回 params
//  6. Execute() 格式化答案返回给 LLM
type AskUser struct {
	info *ToolInfo // 工具元信息
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
			Prompt: `Use this tool when you need to ask the user questions during execution. This allows you to:
	1. Gather user preferences or requirements
	2. Clarify ambiguous instructions
	3. Get decisions on implementation choices as you work
	4. Offer choices to the user about what direction to take.

Usage notes:
- Users will always be able to select "Other" to provide custom text input
- Use multiSelect: true to allow multiple answers to be selected for a question
- If you recommend a specific option, make that the first option in the list and add "(Recommended)" at the end of the label`,
			Tags:          []string{"interaction", "question", "clarify", "human"},
			IsReadOnly:    false,
			SecurityLevel: events.LevelSensitive,
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

// Execute 执行用户提问操作。
// 这是一个恒等函数：实际的交互（显示问题对话框、收集用户答案）
// 由执行器的 awaitUserResponse 通过 AskUserRequest 事件处理。
// 用户答案通过 params["answers"] 注入。
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "question"，可选 "options" 和 "multiSelect"
//
// 返回：
//   - string: 格式化的答案结果或提示消息
//   - error: 缺少必需参数时返回错误
func (t *AskUser) Execute(ctx context.Context, params map[string]any) (any, error) {
	question, ok := params["question"].(string)
	if !ok || question == "" {
		return nil, fmt.Errorf("missing required parameter: question")
	}

	// If answers are present (injected via permission UpdatedInput), format them
	if answers, ok := params["answers"].(map[string]any); ok && len(answers) > 0 {
		return formatAnswerResult(question, answers), nil
	}

	// Fallback: permission was granted without explicit answers (e.g., auto-allow)
	return fmt.Sprintf(`Asked user: "%s". Proceed based on the response.`, question), nil
}

// formatAnswerResult 构建自然语言的答案结果字符串。
// 遵循 Claude Code 的方式，告诉模型如何处理答案。
// 键按字母顺序排序以确保确定性输出。
//
// 参数：
//   - question: 原始问题
//   - answers: 用户答案映射（键为问题标识，值为答案）
//
// 返回：
//   - string: 格式化的答案结果
func formatAnswerResult(question string, answers map[string]any) string {
	// Sort keys for deterministic output
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, q := range keys {
		parts = append(parts, fmt.Sprintf(`"%s" = "%v"`, q, answers[q]))
	}
	return fmt.Sprintf(
		"User has answered your questions: %s. You can now continue with the user's answers in mind.",
		strings.Join(parts, ", "),
	)
}

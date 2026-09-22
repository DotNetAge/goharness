package events

type AskUserQuestion struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select"`
}

type AskUserRequestData struct {
	TickID    string            `json:"tick_id"`
	Questions []AskUserQuestion `json:"questions"`

	reply func(answers map[string]string)
}

func (d *AskUserRequestData) Reply(answers map[string]string) {
	if d.reply != nil {
		d.reply(answers)
	}
}

func NewAskUserRequestData(tickID string, questions []AskUserQuestion, replyFn func(map[string]string)) AskUserRequestData {
	return AskUserRequestData{
		TickID:    tickID,
		Questions: questions,
		reply:     replyFn,
	}
}

// AskUserPendingData 携带非阻塞式 AskUser 交互的问题数据。
// 思考循环已被暂停，用户将通过常规用户消息进行回复。
type AskUserPendingData struct {
	Questions []AskUserQuestion `json:"questions"`

	// SessionID 是发起提问的会话 ID。
	// 子智能体提问冒泡场景下为子会话 ID：前端作答时携带该 ID 发送普通消息，
	// daemon 据此把答案精确路由到挂起等待的子 exec（askCh），避免多个子会话
	// 并发提问时回答错位。主会话自身的提问不设置（为空），保持旧行为。
	SessionID string `json:"session_id,omitempty"`

	// ToolCallID 是本轮 AskUser 工具调用的 ID：恢复时回答以 AskUser 工具结果
	// 消息（Role=tool）补全会话，保证 assistant.tool_calls 与 tool 消息严格
	// 配对，且提问-回答上下文对 LLM 完整可见。为空时（异常兜底）退回旧的
	// user 消息注入方式。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

func NewAskUserPendingData(questions []AskUserQuestion) AskUserPendingData {
	return AskUserPendingData{
		Questions: questions,
	}
}

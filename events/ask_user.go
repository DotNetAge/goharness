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
}

func NewAskUserPendingData(questions []AskUserQuestion) AskUserPendingData {
	return AskUserPendingData{
		Questions: questions,
	}
}

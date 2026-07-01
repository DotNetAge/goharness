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

// AskUserPendingData carries the question data for a non-blocking AskUser
// interaction. The thinking loop has been paused and the user will respond
// via a regular user message.
type AskUserPendingData struct {
	Questions []AskUserQuestion `json:"questions"`
}

func NewAskUserPendingData(questions []AskUserQuestion) AskUserPendingData {
	return AskUserPendingData{
		Questions: questions,
	}
}

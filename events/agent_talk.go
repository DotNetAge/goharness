package events

type AgentTalkInfo struct {
	To        string `json:"to"`
	SessionID string `json:"session_id"`
	Message   string `json:"message,omitempty"`
}

type AgentTalkResult struct {
	To        string `json:"to"`
	SessionID string `json:"session_id"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
}

package events

type SubtaskInfo struct {
	AgentName   string `json:"agent_name,omitempty"`
	Description string `json:"description"`
	Timeout     string `json:"timeout,omitempty"`
	SessionID   string `json:"session_id"`
}

type SubtaskResult struct {
	AgentName   string `json:"agent_name,omitempty"`
	Success     bool   `json:"success"`
	Answer      string `json:"answer,omitempty"`
	Error       string `json:"error,omitempty"`
	Description string `json:"description,omitempty"`
	SessionID   string `json:"session_id"`
}

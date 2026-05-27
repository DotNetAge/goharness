package events

type SubtaskInfo struct {
	TaskID      string `json:"task_id"`
	AgentName   string `json:"agent_name,omitempty"`
	Description string `json:"description"`
	Timeout     string `json:"timeout,omitempty"`
}

type SubtaskResult struct {
	TaskID  string `json:"task_id"`
	Success bool   `json:"success"`
	Answer  string `json:"answer,omitempty"`
	Error   string `json:"error,omitempty"`
}

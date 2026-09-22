package events

type SubtaskInfo struct {
	AgentName   string `json:"agent_name,omitempty"`
	Description string `json:"description"`
	Timeout     string `json:"timeout,omitempty"`
	SessionID   string `json:"session_id"`
	// TaskID 是子任务的跟踪句柄（控制平面运行实例 ID），
	// CollectResults 据此等待并收集结果。
	TaskID string `json:"task_id,omitempty"`
}

type SubtaskResult struct {
	AgentName   string `json:"agent_name,omitempty"`
	Success     bool   `json:"success"`
	Answer      string `json:"answer,omitempty"`
	Error       string `json:"error,omitempty"`
	Description string `json:"description,omitempty"`
	SessionID   string `json:"session_id"`
	// TaskID 是子任务的跟踪句柄，与 SubtaskInfo.TaskID 配对。
	TaskID string `json:"task_id,omitempty"`
}

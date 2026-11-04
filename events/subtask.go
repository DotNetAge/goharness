package events

type SubtaskInfo struct {
	AgentName   string `json:"agent_name,omitempty"`
	Description string `json:"description"`
	Timeout     string `json:"timeout,omitempty"`
	SessionID   string `json:"session_id"`
	// Title 是受理即时标题快照（任务文本截断）；titleFn 注入时后台提炼
	// 更准确的标题并经 SubtaskTitleUpdated 事件刷新。
	Title string `json:"title,omitempty"`
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

// SubtaskTitle 是子代理会话标题刷新事件（SubtaskTitleUpdated）的数据：
// 受理时的快照标题（任务截断）被后台提炼标题替换。
type SubtaskTitle struct {
	AgentName string `json:"agent_name,omitempty"`
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`
	Title     string `json:"title"`
}

package session

// CompactionEvent 携带会话压缩事件的详细信息。
// 它被传递给 CompactionHandler 回调，以通知订阅者有关上下文窗口管理活动的信息。
type CompactionEvent struct {
	// MessagesSlid 指示从活跃窗口中移除了多少条消息
	MessagesSlid int `json:"messages_slid"`

	// RemainingAfter 显示活跃窗口中仍剩余的消息数
	RemainingAfter int `json:"remaining_after"`

	// WindowSize 是配置的最大窗口大小（以 token 为单位）
	WindowSize int64 `json:"window_size"`
}

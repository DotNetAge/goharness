package events

// CompactionData 携带会话压缩事件的详细信息。
type CompactionData struct {
	SessionID      string `json:"session_id"`
	MessagesSlid   int    `json:"messages_slid"`
	RemainingAfter int    `json:"remaining_after"`
	WindowSize     int64  `json:"window_size"`
}

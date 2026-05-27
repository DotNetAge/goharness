package events

// CompactionData carries details about a session compaction event.
type CompactionData struct {
	SessionID      string `json:"session_id"`
	MessagesSlid   int    `json:"messages_slid"`
	RemainingAfter int    `json:"remaining_after"`
	WindowSize     int64  `json:"window_size"`
}

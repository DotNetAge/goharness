package events

// ── 事件类型 ────────────────────────────────────────────────

// 会话级上下文管理事件（compact、micro-compact）。
// 每个事件都有 Start（操作前）和 Done（操作后）两个变体。
// Start 携带操作前的窗口 token 总数。
// Done 携带操作后的窗口 token 数和压缩比。

const (
	// CompactStart 表示 TryCompact（LLM 摘要 + 游标滑动）
	// 即将开始。数据：CompactStartData
	CompactStart ReactEventType = "compact_start"

	// CompactDone 表示 TryCompact 已完成。
	// 数据：CompactDoneData
	CompactDone ReactEventType = "compact_done"

	// MicroCompactStart 表示 TryMicroCompact（工具消息压缩）
	// 即将开始。数据：MicroCompactStartData
	MicroCompactStart ReactEventType = "micro_compact_start"

	// MicroCompactDone 表示 TryMicroCompact 已完成。
	// 数据：MicroCompactDoneData
	MicroCompactDone ReactEventType = "micro_compact_done"
)

// ── 数据结构 ────────────────────────────────────────────

// CompactStartData 携带完整（LLM）压缩开始前的状态。
type CompactStartData struct {
	SessionID     string `json:"session_id"`
	WindowTokens  int64  `json:"window_tokens"`   // 压缩前的 token 总数
	MaxWindowSize int64  `json:"max_window_size"`
}

// CompactDoneData 携带完整压缩的结果。
type CompactDoneData struct {
	SessionID     string  `json:"session_id"`
	MessagesSlid  int     `json:"messages_slid"`
	WindowTokens  int64   `json:"window_tokens"`   // 压缩后的 token 数（游标滑动后为 0）
	MaxWindowSize int64   `json:"max_window_size"`
	Ratio         float64 `json:"ratio"`            // 压缩后 / 压缩前（窗口清空时为 0）
}

// MicroCompactStartData 携带微压缩开始前的状态。
type MicroCompactStartData struct {
	SessionID     string `json:"session_id"`
	WindowTokens  int64  `json:"window_tokens"`   // 微压缩前的 token 总数
	MaxWindowSize int64  `json:"max_window_size"`
}

// MicroCompactDoneData 携带一次微压缩操作的结果。
type MicroCompactDoneData struct {
	SessionID     string `json:"session_id"`
	Compressed    int     `json:"compressed"`     // 被微压缩的消息数量
	Deduped       int     `json:"deduped"`        // 被移除的重复工具消息数量
	WindowTokens  int64   `json:"window_tokens"`  // 所有操作完成后的窗口 token 估算值
	MaxWindowSize int64   `json:"max_window_size"`
	Ratio         float64 `json:"ratio"`           // 压缩后 / 压缩前
}

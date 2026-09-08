package events

// ── 事件类型 ────────────────────────────────────────────────

// 会话级上下文管理事件（compact）。
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

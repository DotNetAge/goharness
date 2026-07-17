package session

// ContextWindowUsage 保存当前上下文窗口的使用信息。
// 计算方式与 MicroCompact/TryMicroCompact 方法一致。
type ContextWindowUsage struct {
	// WindowTokens 是活跃窗口的估算 token 数。
	WindowTokens int64 `json:"window_tokens"`

	// MaxWindowSize 是配置的最大上下文窗口大小（以 token 为单位）。
	// 如果为 0，则禁用压缩，使用率未定义。
	MaxWindowSize int64 `json:"max_window_size"`

	// UsageRatio 是活跃窗口相对于最大窗口的比例（WindowTokens / MaxWindowSize）。
	// 范围从 0.0 到 1.0+。如果 MaxWindowSize 为 0，则返回 0。
	UsageRatio float64 `json:"usage_ratio"`

	// MessageCount 是会话中的总消息数。
	MessageCount int `json:"message_count"`

	// Cursor 是当前游标位置，分隔历史和活跃消息。
	Cursor int `json:"cursor"`

	// ActiveMessageCount 是活跃窗口中的消息数。
	ActiveMessageCount int `json:"active_message_count"`

	// TotalActualTokens 是活跃窗口中有 Usage 数据的消息（仅助手消息）的 ActualTokens() 总和。
	// 表示当前活跃窗口内排除缓存命中的净 token 消耗。
	TotalActualTokens int64 `json:"total_actual_tokens"`

	// TotalCost 是活跃窗口中有 Usage 数据的消息的 Cost() 总和，使用给定定价计算。
	// 如果未提供定价，则为 0。
	TotalCost float64 `json:"total_cost"`
}

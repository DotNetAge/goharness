package events

// ── Event types ────────────────────────────────────────────────

// Session-level context management events (compact, micro-compact).
// Each has a Start (before) and Done (after) variant.
// Start carries the total window tokens before the operation.
// Done carries the resulting window tokens and the compression ratio.

const (
	// CompactStart signals that TryCompact (LLM summarization + cursor slide)
	// is about to begin.  Data: CompactStartData
	CompactStart ReactEventType = "compact_start"

	// CompactDone signals that TryCompact has completed.
	// Data: CompactDoneData
	CompactDone ReactEventType = "compact_done"

	// MicroCompactStart signals that TryMicroCompact (tool message compression)
	// is about to begin.  Data: MicroCompactStartData
	MicroCompactStart ReactEventType = "micro_compact_start"

	// MicroCompactDone signals that TryMicroCompact has completed.
	// Data: MicroCompactDoneData
	MicroCompactDone ReactEventType = "micro_compact_done"
)

// ── Data structures ────────────────────────────────────────────

// CompactStartData carries the state before a full (LLM) compaction begins.
type CompactStartData struct {
	SessionID     string `json:"session_id"`
	WindowTokens  int64  `json:"window_tokens"`   // total tokens before compaction
	MaxWindowSize int64  `json:"max_window_size"`
}

// CompactDoneData carries the result of a full compaction.
type CompactDoneData struct {
	SessionID     string  `json:"session_id"`
	MessagesSlid  int     `json:"messages_slid"`
	WindowTokens  int64   `json:"window_tokens"`   // tokens after compaction (0 after cursor slide)
	MaxWindowSize int64   `json:"max_window_size"`
	Ratio         float64 `json:"ratio"`            // after / before (0 when window is emptied)
}

// MicroCompactStartData carries the state before micro-compression begins.
type MicroCompactStartData struct {
	SessionID     string `json:"session_id"`
	WindowTokens  int64  `json:"window_tokens"`   // total tokens before micro-compression
	MaxWindowSize int64  `json:"max_window_size"`
}

// MicroCompactDoneData carries the result of a micro-compression pass.
type MicroCompactDoneData struct {
	SessionID     string  `json:"session_id"`
	Compressed    int     `json:"compressed"`     // number of messages micro-compressed
	Deduped       int     `json:"deduped"`        // number of duplicate tool messages removed
	WindowTokens  int64   `json:"window_tokens"`  // estimated window tokens after all ops
	MaxWindowSize int64   `json:"max_window_size"`
	Ratio         float64 `json:"ratio"`           // after / before
}

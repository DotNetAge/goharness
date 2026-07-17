package session

// SessionConfig is a functional option for configuring Session instances.
// This pattern allows for flexible, readable configuration without breaking changes
// when new options are added.
//
// Example:
//
//	session := NewSession("id", "agent",
//	    WithMaxWindowSize(8000),
//	    WithSummarizer(mySummarizer),
//	    WithCompactionHandler(myHandler),
//	)
type SessionConfig func(*Session)

func (s *Session) logInfo(msg string, keyvals ...any) {
	if s.log != nil {
		s.log.Info(msg, keyvals...)
	}
}

func (s *Session) logError(msg string, err error, keyvals ...any) {
	if s.log != nil {
		s.log.Error(msg, err, keyvals...)
	}
}

// WithMemory configures the memory store for context summaries.
// If not set, an in-memory store is used by default.
func WithMemory(mem MemoryStore) SessionConfig {
	return func(s *Session) { s.mem = mem }
}

// WithSummarizer sets the LLM-based summarizer for context compaction.
// When the context window exceeds thresholds, old messages are summarized
// using this component to preserve important information.
func WithSummarizer(ss Summarizer) SessionConfig {
	return func(s *Session) { s.summarizer = ss }
}

// WithMaxWindowSize configures the maximum context window size in tokens.
// When the active window exceeds 80% of this value, compaction is triggered
// to trim it down to ~60% of the maximum.
//
// A value of 0 or negative disables automatic compaction.
func WithMaxWindowSize(n int64) SessionConfig {
	return func(s *Session) { s.maxWindowSize = n }
}

// WithCompactionHandler sets a callback function that is invoked after each
// compaction event. This can be used for logging, metrics, or UI updates.
func WithCompactionHandler(h func(CompactionEvent)) SessionConfig {
	return func(s *Session) { s.compactionHandler = h }
}



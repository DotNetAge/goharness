package memory

// RetrieveConfig holds configuration options for memory retrieval operations.
type RetrieveConfig struct {
	Types     []MemoryType
	SessionID string
	Limit     int
	MinScore  float64
}

// RetrieveOption is a functional option for configuring RetrieveConfig.
type RetrieveOption func(*RetrieveConfig)

// WithMemoryTypes filters retrieval to only include the specified memory types.
func WithMemoryTypes(types ...MemoryType) RetrieveOption {
	return func(c *RetrieveConfig) { c.Types = types }
}

// WithMemoryLimit sets the maximum number of records to return.
// Values <= 0 are ignored (keeps default).
func WithMemoryLimit(n int) RetrieveOption {
	return func(c *RetrieveConfig) {
		if n > 0 {
			c.Limit = n
		}
	}
}

// WithMinScore sets the minimum relevance score for returned records.
func WithMinScore(score float64) RetrieveOption {
	return func(c *RetrieveConfig) { c.MinScore = score }
}

// WithMemorySessionID scopes memory retrieval to a specific session.
// Memory implementations should filter by this field for session-scoped recall.
func WithMemorySessionID(sessionID string) RetrieveOption {
	return func(c *RetrieveConfig) { c.SessionID = sessionID }
}

package memory

type RetrieveConfig struct {
	Types     []MemoryType
	SessionID string
	Limit     int
	MinScore  float64
}

type RetrieveOption func(*RetrieveConfig)

func WithMemoryTypes(types ...MemoryType) RetrieveOption {
	return func(c *RetrieveConfig) { c.Types = types }
}

func WithMemoryLimit(n int) RetrieveOption {
	return func(c *RetrieveConfig) {
		if n > 0 {
			c.Limit = n
		}
	}
}

func WithMinScore(score float64) RetrieveOption {
	return func(c *RetrieveConfig) { c.MinScore = score }
}

// WithMemorySessionID scopes memory retrieval to a specific session.
// Memory implementations should filter by this field for session-scoped recall.
func WithMemorySessionID(sessionID string) RetrieveOption {
	return func(c *RetrieveConfig) { c.SessionID = sessionID }
}

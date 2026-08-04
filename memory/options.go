package memory

// RetrieveConfig 保存记忆检索操作的配置选项。
type RetrieveConfig struct {
	Types     []MemoryType
	SessionID string
	AgentName string
	ProjectDir string
	Limit     int
	MinScore  float64
}

// RetrieveOption 是用于配置 RetrieveConfig 的函数式选项。
type RetrieveOption func(*RetrieveConfig)

// WithMemoryTypes 过滤检索结果，仅包含指定的记忆类型。
func WithMemoryTypes(types ...MemoryType) RetrieveOption {
	return func(c *RetrieveConfig) { c.Types = types }
}

// WithMemoryLimit 设置返回记录的最大数量。
// 值 <= 0 时将被忽略（保持默认值）。
func WithMemoryLimit(n int) RetrieveOption {
	return func(c *RetrieveConfig) {
		if n > 0 {
			c.Limit = n
		}
	}
}

// WithMinScore 设置返回记录的最小相关性分数。
func WithMinScore(score float64) RetrieveOption {
	return func(c *RetrieveConfig) { c.MinScore = score }
}

// WithMemorySessionID 将记忆检索范围限定到特定会话。
// Memory 实现应按此字段过滤以支持会话级召回。
func WithMemorySessionID(sessionID string) RetrieveOption {
	return func(c *RetrieveConfig) { c.SessionID = sessionID }
}

// WithAgentName 将记忆检索范围限定到特定 agent。
// 同时用于短期（AgentName + SessionID）和长期（仅 AgentName）过滤。
func WithAgentName(agentName string) RetrieveOption {
	return func(c *RetrieveConfig) { c.AgentName = agentName }
}

// WithProjectDir 将记忆检索范围限定到特定项目目录。
// 与 AgentName 结合使用，将召回范围限定为同一 agent 在多个会话中
// 处理同一项目（记忆缓冲区的默认范围）。
func WithProjectDir(dir string) RetrieveOption {
	return func(c *RetrieveConfig) { c.ProjectDir = dir }
}

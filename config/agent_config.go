package config

type AgentConfig struct {
	Name                string         `json:"name" yaml:"name"`
	Role                string         `json:"role" yaml:"role"`
	Description         string         `json:"description" yaml:"description"`
	Introduction        string         `json:"introduction" yaml:"introduction"`
	Model               string         `json:"model" yaml:"model"`
	Skills              []string       `json:"skills,omitempty" yaml:"skills,omitempty"`
	Body                string         `json:"body,omitempty" yaml:"body,omitempty"`
	EnableOrchestration bool           `json:"enable_orchestration" yaml:"enable_orchestration"`
	MaxDecomposeDepth   int            `json:"max_decompose_depth,omitempty" yaml:"max_decompose_depth,omitempty"`
	Meta                map[string]any `json:"meta,omitempty" yaml:"meta,omitempty"`
}

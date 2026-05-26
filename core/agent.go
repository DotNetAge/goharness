package core

// AgentConfig defines an agent's identity and behavior configuration.
//
// ## Three-layer model (see design doc Section 2.1):
//
//   - **Name**: Always loaded by Orchestrator + Agent.
//   - **Description** (≤1024 chars): Role description for routing + self-judgment.
//     Loaded by Orchestrator during routing AND by Agent during responsibility check.
//     Never includes full system prompt details — that's Body's job.
//   - **Body**: Full System Prompt / instruction set. Only loaded into the Agent's
//     ContextWindow when the Agent executes a task (via Think-Act loop Level 2/3).
//
// ## Orchestration fields (optional, zero-value defaults):
//
// When EnableOrchestration=false (default), the agent behaves identically to the
// pre-orchestration version — no responsibility gate, no coordinator mode,
// no orchestrator communication.
type AgentConfig struct {
	Name         string   `json:"name" yaml:"name"`                         // Name is the agent name
	Role         string   `json:"role" yaml:"role"`                         // Agent act as a role
	Description  string   `json:"description" yaml:"description"`           // Description: role/capability description (≤1024 chars). Used by Orchestrator routing AND Agent self-judgment.
	Introduction string   `json:"introduction" yaml:"introduction"`         // Introduction: alias for system prompt / body content. When empty, rendered from template.
	Model        string   `json:"model" yaml:"model"`                       // Model is the model name
	Skills       []string `json:"skills,omitempty" yaml:"skills,omitempty"` // Skill names this agent requires. Activated when the agent is spawned.

	// --- Orchestration fields (all optional, safe zero-values) ---
	Body                string         `json:"body,omitempty" yaml:"body,omitempty"`                               // Full system prompt body. Only loaded into ContextWindow during task execution.
	EnableOrchestration bool           `json:"enable_orchestration" yaml:"enable_orchestration"`                   // Enable orchestration mode (responsibility gate + coordinator). Default: false.
	MaxDecomposeDepth   int            `json:"max_decompose_depth,omitempty" yaml:"max_decompose_depth,omitempty"` // Maximum WBS decomposition depth. Default: 2.
	Meta                map[string]any `json:"meta,omitempty" yaml:"meta,omitempty"`                               // Extensible metadata for custom fields. Synced to/from YAML frontmatter.
}

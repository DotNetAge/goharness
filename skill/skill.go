package skill

import "errors"

// Skill errors
var (
	ErrSkillNotFound    = errors.New("skill not found")
	ErrSkillExecution   = errors.New("skill execution failed")
	ErrSkillCompilation = errors.New("skill compilation failed")

// Skill represents a specialized capability that extends the agent's behavior.
// It follows the Agent Skills specification (agentskills.io) for discovery and loading.
//
// A Skill is loaded from a directory containing a SKILL.md file with YAML frontmatter
// and Markdown body (instructions). The spec defines a three-level progressive disclosure:
//  1. Metadata (~100 tokens): name and description loaded at startup
//  2. Instructions (< 5000 tokens recommended): SKILL.md body loaded on activation
//  3. Resources (as needed): files in scripts/, references/, assets/ loaded on demand
)

type Skill struct {
	// --- Spec-required fields (from SKILL.md frontmatter) ---

	Name        string `json:"name" yaml:"name"`               // Required. Max 64 chars. Lowercase letters, numbers, hyphens only.
	Description string `json:"description" yaml:"description"` // Required. Max 1024 chars. What the skill does and when to use it.

	// --- Spec-optional fields ---
	Paths         []string          `json:"paths,omitempty" yaml:"paths,omitempty"`                 // Optional. List of relative paths to files or directories to load.
	License       string            `json:"license,omitempty" yaml:"license,omitempty"`             // License name or reference to bundled license file.
	Compatibility string            `json:"compatibility,omitempty" yaml:"compatibility,omitempty"` // Environment requirements (max 500 chars).
	Metadata      map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`           // Arbitrary key-value metadata.
	AllowedTools  string            `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"` // Space-separated pre-approved tools (experimental).

	// --- Instructions (Markdown body after frontmatter) ---

	Instructions string `json:"instructions" yaml:"-"` // Markdown-formatted instructions loaded from SKILL.md body.

	// --- Runtime fields (not from spec, used internally) ---

	RootDir string `json:"-"`                // Absolute path to the skill directory on disk (empty for bundled/embedded skills).
	Source  string `json:"source,omitempty"` // "bundled" or "filesystem".
}

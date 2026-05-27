package events

// SecurityLevel represents the security classification of a tool or action.
// Security levels are used to determine authorization requirements and
// risk assessment for operations that may have side effects.
// Higher security levels require more stringent authorization checks.
type SecurityLevel int

const (
	// LevelSafe indicates pure query operations with no side effects.
	// These operations are read-only and do not modify any state.
	// Examples: reading files, querying databases, searching the web.
	LevelSafe SecurityLevel = iota
	// LevelSensitive indicates operations with bounded write effects.
	// These operations may modify state but with predictable, limited scope.
	// Examples: creating temporary files, updating user preferences, sending messages.
	LevelSensitive
	// LevelHighRisk indicates operations with unpredictable or destructive effects.
	// These operations may cause significant changes and require explicit authorization.
	// Examples: deleting files, executing arbitrary code, modifying system configuration.
	LevelHighRisk
)

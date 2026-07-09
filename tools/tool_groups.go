package tools

// toolGroupMaps defines tool group expansions.
// When a tool name maps to a non-empty list, selecting that tool
// automatically activates all tools in the group.
//
// Groups are defined for tools that share a data model and
// lifecycle — where using one member alone has no independent value.
//
// This mapping is internal-only. It is not visible to LLM
// (Tool Catalog remains flat) and does not affect external APIs.
var toolGroupMaps = map[string][]string{
	// Task group: CRUD lifecycle on shared Task data model
	"TaskCreate": {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskGet":    {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskList":   {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},
	"TaskUpdate": {"TaskCreate", "TaskGet", "TaskList", "TaskUpdate"},

	// Team group: lifecycle on shared Team data model
	"TeamCreate":    {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamDelete":    {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamList":      {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
	"TeamGetTasks":  {"TeamCreate", "TeamDelete", "TeamList", "TeamGetTasks"},
}

// ExpandGroup returns the full group for a tool name.
// If the tool is not in any group, returns a slice with only the tool itself.
func ExpandGroup(name string) []string {
	if group, ok := toolGroupMaps[name]; ok {
		return group
	}
	return []string{name}
}

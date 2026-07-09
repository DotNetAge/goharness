package agents

import (
	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goharness/tools"
)

// ActiveToolSet manages the set of tools currently exposed to the LLM
// in the `tools` field of each request. It is scoped to a single Ask()
// call and resets on each new Ask() invocation (Turn-level lifecycle).
//
// The set always includes:
//   - Core tools: ToolSelector, Skill, AskUser
//   - Conditional tools: SubAgent, CollectResults (only when SubAgent is configured)
//
// Additional tools are activated via ToolSelector or Skill AllowedTools
// and persist for the remainder of the Turn.
//
// ActiveToolSet is NOT persisted to session and NOT shared across Turns.
type ActiveToolSet struct {
	// core are the always-present tools (ToolSelector, Skill, AskUser).
	core []string
	// conditional are tools that are present only when the agent config
	// includes SubAgent (SubAgent, CollectResults).
	conditional []string
	// activated are tools loaded during this Turn via ToolSelector or Skill.
	activated map[string]struct{}

	// registry is the ToolRegistry used to build tool definitions for LLM.
	registry tools.ToolRegistry
}

// NewActiveToolSet creates an ActiveToolSet with the given core and conditional tools.
// hasSubAgent determines whether conditional tools (SubAgent, CollectResults) are included.
func NewActiveToolSet(registry tools.ToolRegistry, hasSubAgent bool) *ActiveToolSet {
	ats := &ActiveToolSet{
		core:      []string{"ToolSelector", "Skill", "AskUser"},
		activated: make(map[string]struct{}),
		registry:  registry,
	}
	if hasSubAgent {
		ats.conditional = []string{"SubAgent", "CollectResults"}
	}
	return ats
}

// Activate adds tools to the active set. Each name goes through group expansion.
// Returns the final list of tool names that were activated (including group expansions).
// Unknown tool names (not in registry) are silently skipped.
func (a *ActiveToolSet) Activate(names []string) []string {
	var activated []string
	seen := make(map[string]struct{})
	for _, name := range names {
		group := tools.ExpandGroup(name)
		for _, member := range group {
			if _, ok := seen[member]; ok {
				continue
			}
			if _, ok := a.registry.Get(member); !ok {
				continue
			}
			seen[member] = struct{}{}
			a.activated[member] = struct{}{}
			activated = append(activated, member)
		}
	}
	return activated
}

// Reset clears all activated tools, restoring to core + conditional only.
// Called at the beginning of each new Ask().
func (a *ActiveToolSet) Reset() {
	a.activated = make(map[string]struct{})
}

// BuildDefinitions returns the tool definitions for all currently active tools,
// ready to be sent in the LLM request `tools` field.
func (a *ActiveToolSet) BuildDefinitions() []gochatcore.Tool {
	names := a.allNames()
	if len(names) == 0 {
		return nil
	}
	out := make([]gochatcore.Tool, 0, len(names))
	for _, name := range names {
		t, ok := a.registry.Get(name)
		if !ok {
			continue
		}
		info := t.Info()
		out = append(out, gochatcore.Tool{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  buildParamSchema(info.Parameters),
		})
	}
	return out
}

// Has returns whether the given tool is currently active.
func (a *ActiveToolSet) Has(name string) bool {
	for _, n := range a.allNames() {
		if n == name {
			return true
		}
	}
	return false
}

// allNames returns all active tool names in order: core, conditional, activated.
func (a *ActiveToolSet) allNames() []string {
	var names []string
	names = append(names, a.core...)
	names = append(names, a.conditional...)
	for name := range a.activated {
		names = append(names, name)
	}
	return names
}

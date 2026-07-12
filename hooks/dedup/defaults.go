package dedup

// DefaultPolicies returns the built-in Phase 1 deduplication policies.
//
// Phase 1 tools are idempotent/read-only operations whose results are
// guaranteed to be stable within a session.  The key set per tool was
// chosen to capture the minimum parameters that uniquely identify a call.
func DefaultPolicies() []DedupPolicy {
	return []DedupPolicy{
		&simplePolicy{tool: "WebSearch", keys: []string{"query"}},
		&simplePolicy{tool: "WebFetch", keys: []string{"url"}},
		&simplePolicy{tool: "Glob", keys: []string{"pattern"}},
		&simplePolicy{tool: "Grep", keys: []string{"pattern", "path"}},
		&simplePolicy{tool: "Ls", keys: []string{"path"}},
		&simplePolicy{tool: "Skill", keys: []string{"name", "input"}},
	}
}

// simplePolicy is a DedupPolicy that uses only a declared subset of
// parameters to compute the ContentKey.
type simplePolicy struct {
	tool string
	keys []string
}

func (p *simplePolicy) ToolName() string { return p.tool }

func (p *simplePolicy) ContentKey(params map[string]any) string {
	filtered := make(map[string]any, len(p.keys))
	for _, k := range p.keys {
		if v, ok := params[k]; ok {
			filtered[k] = v
		}
	}
	return NormalizeContentKey(p.tool, filtered)
}

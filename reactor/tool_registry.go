package reactor

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/DotNetAge/goreact/core"
)

type DefaultToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]core.FuncTool
}

var _ core.ToolRegistry = (*DefaultToolRegistry)(nil)

func NewDefaultToolRegistry() *DefaultToolRegistry {
	return &DefaultToolRegistry{
		tools: make(map[string]core.FuncTool),
	}
}

func NewToolRegistry() *DefaultToolRegistry {
	return NewDefaultToolRegistry()
}

func (r *DefaultToolRegistry) Register(tool core.FuncTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Info().Name
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = tool
	return nil
}

func (r *DefaultToolRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	delete(r.tools, name)
	return nil
}

func (r *DefaultToolRegistry) Get(name string) (core.FuncTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *DefaultToolRegistry) All() []core.FuncTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.FuncTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

func (r *DefaultToolRegistry) FindAvailable(filter *core.ToolFilter) []core.FuncTool {
	if filter == nil || (len(filter.Keywords) == 0 && len(filter.AllowedNames) == 0 && filter.Security == 0 && filter.Terms == "") {
		return r.All()
	}

	allTools := r.All()

	if len(filter.AllowedNames) > 0 {
		allowedSet := make(map[string]bool, len(filter.AllowedNames))
		for _, n := range filter.AllowedNames {
			allowedSet[n] = true
		}
		var filtered []core.FuncTool
		for _, t := range allTools {
			if allowedSet[t.Info().Name] {
				filtered = append(filtered, t)
			}
		}
		return filtered
	}

	lowerKeywords := make(map[string]bool, len(filter.Keywords))
	for _, kw := range filter.Keywords {
		lowerKeywords[strings.ToLower(strings.TrimSpace(kw))] = true
	}

	var matched []core.FuncTool
	for _, t := range allTools {
		info := t.Info()
		if r.toolMatchesFilter(info, filter, lowerKeywords) {
			matched = append(matched, t)
		}
	}

	if len(matched) == 0 {
		log.Printf("tool registry: filter matched no tools, returning all %d tools (filter may be too restrictive)", len(allTools))
		return allTools
	}
	return matched
}

func (r *DefaultToolRegistry) toolMatchesFilter(info *core.ToolInfo, filter *core.ToolFilter, keywords map[string]bool) bool {

	if filter.Security != 0 && info.SecurityLevel != filter.Security {
		return false
	}

	if len(keywords) > 0 {
		for _, tag := range info.Tags {
			if keywords[strings.ToLower(tag)] {
				return true
			}
		}
		nameLower := strings.ToLower(info.Name)
		descLower := strings.ToLower(info.Description)
		for kw := range keywords {
			if strings.Contains(nameLower, kw) || strings.Contains(descLower, kw) {
				return true
			}
		}
		return false
	}

	if filter.Terms != "" {
		termsLower := strings.ToLower(filter.Terms)
		descLower := strings.ToLower(info.Description)
		if strings.Contains(descLower, termsLower) {
			return true
		}
		for _, tag := range info.Tags {
			if strings.Contains(strings.ToLower(tag), termsLower) {
				return true
			}
		}
		return false
	}

	return true
}

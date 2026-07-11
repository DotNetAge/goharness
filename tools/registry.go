package tools

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/logging"
)

// DefaultToolRegistry is the default implementation of ToolRegistry.
// It provides thread-safe tool registration, lookup, and filtering capabilities.
//
// The registry maintains a map of tools keyed by their name and supports:
//   - Registration and removal of tools
//   - Lookup by name
//   - Filtering based on keywords, security level, allowed names, or search terms
//   - Thread-safe concurrent access via sync.RWMutex
type DefaultToolRegistry struct {
	mu     sync.RWMutex
	tools  map[string]FuncTool
	logger logging.Logger
}

var _ ToolRegistry = (*DefaultToolRegistry)(nil)

// NewDefaultToolRegistry creates a new empty DefaultToolRegistry with default logger.
// The returned registry is ready for tool registration and concurrent use.
func NewDefaultToolRegistry() *DefaultToolRegistry {
	return &DefaultToolRegistry{
		tools:  make(map[string]FuncTool),
		logger: logging.DefaultLogger(),
	}
}

// Register adds a tool to the registry.
// Returns an error if a tool with the same name is already registered.
// The tool's Info().Name is used as the registry key.
func (r *DefaultToolRegistry) Register(tool FuncTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Info().Name
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("工具 %q 已注册", name)
	}
	r.tools[name] = tool
	return nil
}

// Remove deletes a tool from the registry by name.
// Returns an error if no tool with the given name is found.
func (r *DefaultToolRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return fmt.Errorf("未找到工具 %q", name)
	}
	delete(r.tools, name)
	return nil
}

// Get retrieves a tool by name.
// Returns the tool and true if found, or zero value and false if not found.
func (r *DefaultToolRegistry) Get(name string) (FuncTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All returns a slice of all registered tools.
// The order of tools is non-deterministic (map iteration order).
func (r *DefaultToolRegistry) All() []FuncTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FuncTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// FindAvailable returns tools that match the given filter criteria.
//
// Filtering logic (in order of precedence):
//   - If filter is nil or empty, returns all tools
//   - If AllowedNames is specified, only tools in that list are returned (exact name match)
//   - Otherwise, filters by Keywords (matched against tags, name, description),
//     Security level, and Terms (searched in description and tags)
//
// If keyword/term filtering matches no tools, a warning is logged and all tools are returned.
func (r *DefaultToolRegistry) FindAvailable(filter *ToolFilter) []FuncTool {
	if filter == nil || (len(filter.Keywords) == 0 && len(filter.AllowedNames) == 0 && filter.Security == 0 && filter.Terms == "") {
		return r.All()
	}

	allTools := r.All()

	if len(filter.AllowedNames) > 0 {
		allowedSet := make(map[string]bool, len(filter.AllowedNames))
		for _, n := range filter.AllowedNames {
			allowedSet[n] = true
		}
		var filtered []FuncTool
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

	var matched []FuncTool
	for _, t := range allTools {
		info := t.Info()
		if r.toolMatchesFilter(info, filter, lowerKeywords) {
			matched = append(matched, t)
		}
	}

	if len(matched) == 0 {
		r.logger.Warn("tool registry: filter matched no tools, returning all tools",
			"tool_count", len(allTools),
		)
		return allTools
	}
	return matched
}

// toolMatchesFilter checks if a tool's info matches the filter criteria.
// All conditions are AND-combined: the tool must pass all non-zero filter fields.
//
// Matching rules:
//   - Security: exact match required if specified
//   - Keywords: matched case-insensitively against tags (any match = pass),
//     then against name and description (substring match)
//   - Terms: searched case-insensitively in description and tags
func (r *DefaultToolRegistry) toolMatchesFilter(info *ToolInfo, filter *ToolFilter, keywords map[string]bool) bool {

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

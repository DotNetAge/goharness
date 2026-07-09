package tools

import (
	"context"
	"fmt"
	"strings"
)

// ToolSelectorTool lets the LLM load tool schemas on demand by name.
//
// The LLM sees a flat Tool Catalog in System Prompt (name + description).
// When it determines which tools it needs, it calls ToolSelector with
// the exact tool names. The system then loads those tools' schemas into
// the next request's `tools` field.
//
// Group expansion: selecting any member of a tool group (Task/Team)
// automatically activates the entire group — see ExpandGroup().
type ToolSelectorTool struct {
	registry ToolRegistry
}

// NewToolSelectorTool creates a ToolSelectorTool.
func NewToolSelectorTool(registry ToolRegistry) *ToolSelectorTool {
	return &ToolSelectorTool{registry: registry}
}

func (t *ToolSelectorTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "ToolSelector",
		MaxResultSizeChars: 1000,
		Description:        "Select tools to load into the conversation. Provide exact tool names from the Tool Catalog to make them available for use.",
		Prompt: `Select tools to load into the conversation. Only tools listed in the Tool Catalog can be selected.

When you need tools that are not yet available:
1. Review the Tool Catalog section to find the tool names you need
2. Call ToolSelector with the exact names of the tools you want to load
3. After a successful selection, use the loaded tools in subsequent calls

Select multiple tools at once when you know you will need them to minimize round trips.`,
		Tags:       []string{"tool", "selector", "meta"},
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "names",
				Type:        "array",
				Description: "Exact tool names to load, as listed in the Tool Catalog. Example: [\"Read\", \"Grep\", \"Glob\"].",
				Required:    true,
			},
		},
	}
}

func (t *ToolSelectorTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawNames, ok := params["names"].([]any)
	if !ok || len(rawNames) == 0 {
		return nil, fmt.Errorf("names is required and must be a non-empty array of tool name strings")
	}

	// Deduplicate
	var requested []string
	seen := make(map[string]bool)
	for _, raw := range rawNames {
		name, ok := raw.(string)
		if !ok || name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		requested = append(requested, name)
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("no valid tool names provided")
	}

	// Expand groups and validate against registry
	var loaded []string
	var notFound []string
	expanded := make(map[string]bool)
	for _, name := range requested {
		group := ExpandGroup(name)
		for _, member := range group {
			if expanded[member] {
				continue
			}
			if _, ok := t.registry.Get(member); !ok {
				notFound = append(notFound, member)
				continue
			}
			expanded[member] = true
			loaded = append(loaded, member)
		}
	}

	if len(loaded) == 0 {
		return nil, fmt.Errorf("none of the requested tools are available: %s", strings.Join(requested, ", "))
	}

	// Build human-readable message (for LLM context)
	message := fmt.Sprintf("已加载工具：%s。", strings.Join(loaded, "、"))
	if len(notFound) > 0 {
		message += fmt.Sprintf("\n以下工具不可用或不存在：[%s]", strings.Join(notFound, ", "))
	}

	// Return structured result so ToolActivationHook can parse `loaded` list.
	// The executor serializes this to JSON; the hook deserializes it back.
	return map[string]any{
		"loaded":    loaded,
		"not_found": notFound,
		"message":   message,
	}, nil
}

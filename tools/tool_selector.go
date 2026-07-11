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
		Description:        "选择要加载到对话中的工具。提供工具目录中的确切工具名称以使其可用。",
		Prompt: `选择要加载到对话中的工具。只能选择工具目录中列出的工具。

当你需要尚未可用的工具时：
1. 查看工具目录部分以找到你需要的工具名称
2. 使用你想要加载的工具的确切名称调用 ToolSelector
3. 成功选择后，在后续调用中使用已加载的工具

当你知道需要多个工具时，一次性选择多个工具以减少往返次数。`,
		Tags:       []string{"tool", "selector", "meta"},
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "names",
				Type:        "array",
				Description: "要加载的确切工具名称，如工具目录中列出的。示例：[\"Read\", \"Grep\", \"Glob\"]。",
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
		return nil, fmt.Errorf("未提供有效的工具名称")
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
		return nil, fmt.Errorf("请求的工具均不可用：%s", strings.Join(requested, ", "))
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

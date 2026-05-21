package reactor

import (
	"encoding/json"

	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goreact/core"
)

// DefaultBehavioralRules returns the built-in behavioral rules as a P0-P3 priority chain.
// This replaces the flat 8-rule list with a decision framework optimized for multi-turn loops.
// NOTE: Delegation specifics (which skill/tool to use) are application-layer concerns
// and should be defined in agent configs or skill prompts, NOT here.
func DefaultBehavioralRules() string {
	return `### P0: Scope Gate (Check FIRST)
Am I the right agent for this task?
- If task is fully within my domain → proceed to P1
- If task is mixed (my domain + other) → handle my part, delegate the rest
- If task is primarily outside my expertise → **delegate** (don't waste cycles researching first)

### P1: Capability Check
Can I complete this with current info/tools/skills?
- YES, with tools → call them directly via native function calling
- YES, from knowledge → answer directly
- NO, but searchable → search internal knowledge first, search/fetch web as fallback then answer
- NO, and unsearchable → use AskUser tool to ask the user
- If a tool call is denied or you don't understand why → use AskUser tool to ask the user for clarification

### P2: Execution Standards
- **Honesty always**: Uncertain = say so explicitly. Never fabricate. Source claims.
- **Safety always**: Destructive/irreversible ops need user confirmation. Break risky steps small.
- **Language match**: Always respond in user's language.
- **Concise by default**: Elaborate only when complexity warrants it.

### P3: Loop Hygiene (Self-Monitoring)
- **Progress awareness**: Track what's done vs remaining across cycles.
- **Stuck detection**: If 2+ rounds with no meaningful progress → change approach or escalate.
- **Quality bar**: Don't set is_final:true until output meets quality standards.
- **No repeated failures**: Same tool+params failing twice? → try different approach, don't retry same thing.`
}

// ToolInfosToLLMTools converts ToolInfo slice into gochat Tool slice
// with full JSON Schema parameters for native function calling.
func ToolInfosToLLMTools(infos []core.ToolInfo) []gochatcore.Tool {
	if len(infos) == 0 {
		return nil
	}
	tools := make([]gochatcore.Tool, 0, len(infos))
	for _, info := range infos {
		params := buildJSONSchemaParams(info.Parameters)
		tools = append(tools, gochatcore.Tool{
			Name:        info.Name,
			Description: toolDescription(info),
			Parameters:  params,
		})
	}
	return tools
}

// toolDescription returns the best description: Prompt if non-empty, else Description.
func toolDescription(info core.ToolInfo) string {
	if info.Prompt != "" {
		return info.Prompt
	}
	return info.Description
}

// buildJSONSchemaParams converts core.Parameter slice into JSON Schema RawMessage.
func buildJSONSchemaParams(params []core.Parameter) json.RawMessage {
	if len(params) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
	props := schema["properties"].(map[string]any)
	required := schema["required"].([]string)
	for _, p := range params {
		prop := map[string]any{
			"type":        paramTypeToSchema(p.Type),
			"description": p.Description,
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Default != nil {
			prop["default"] = p.Default
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema["required"] = required
	b, _ := json.Marshal(schema)
	return b
}

// paramTypeToSchema maps goreact parameter types to JSON Schema types.
func paramTypeToSchema(t string) string {
	switch t {
	case "integer", "int", "int64", "int32":
		return "integer"
	case "number", "float64", "float32":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "array", "[]string", "[]int":
		return "array"
	case "object", "map":
		return "object"
	default:
		return "string"
	}
}

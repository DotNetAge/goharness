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
}
- NO, and unsearchable → ask user

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

// // BuildAgentCoordinationGuidance returns the system prompt section for agent orchestration tools.
// func BuildAgentCoordinationGuidance() string {
// 	return `## Agent Coordination

// Agent coordination has two purposes: (a) handing off tasks that fall outside your role to a specialist, and (b) parallelizing large workloads by dispatching independent sub-tasks to multiple agents simultaneously.

// Do NOT use these tools for tasks you can handle directly. Your first responsibility is to complete the work yourself.

// ### When to delegate to another agent
// - The user asks for something that is not in your area of expertise (e.g. you are a code reviewer and they ask for legal advice).
// - The task requires a specialized capability you do not have access to.
// - The user explicitly requests that another agent handle the task.

// ### When to parallelize by spawning multiple agents
// - The current task involves many independent sub-tasks that could run in parallel (e.g. reviewing 10 files, researching 5 topics, testing 3 configurations).
// - You estimate that the total task would take significantly longer if done sequentially — dispatching sub-tasks to agents with the same capabilities as yourself can reduce wall-clock time.
// - Each sub-task is self-contained and does not depend on results from other sub-tasks.

// In those cases, call Delegate multiple times in the same Act phase with different sub-tasks — they will run in parallel. Use CollectResults to gather all outcomes.
// `
// }

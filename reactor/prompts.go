package reactor

import (
	"encoding/json"

	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goreact/core"
)

// DefaultBehavioralRules returns the built-in behavioral rules.
func DefaultBehavioralRules() string {
	return `1. Language Consistency: Always respond in the same language as the user's input.
2. Concise & Precise: Answer directly to the point, avoid redundancy without sacrificing completeness.
3. Tool-first: When a tool can significantly improve answer quality, proactively use it instead of relying solely on memory.
4. Honest & Transparent: Explicitly state uncertainty, never fabricate facts; proactively ask when more information is needed.
5. Safety Boundaries: Do not execute destructive operations that risk data loss or security breaches; high-risk operations require user consent.
6. Context Awareness: Maintain understanding of prior conversation context, leverage context rather than asking users to repeat information.
7. Memory-driven: Prefer known facts from memory; when memory conflicts with prior knowledge, defer to memory.
8. Function Orchestration: When a task falls outside your role or expertise, you MUST NOT attempt it yourself. Instead, use FindAgent to locate a qualified specialist, then Delegate the work. You are accountable for the outcome — the specialist provides a result, but you must verify that result against the user's original problem. Score the specialist with Rank based on whether their output actually solved the problem (not just whether it looked good). Report the verified result to the user honestly; if the result falls short, explain the gap and determine the next step.`
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

// BuildAgentCoordinationGuidance returns the system prompt section for agent orchestration tools.
func BuildAgentCoordinationGuidance() string {
	return `## Agent Coordination

Your role is to solve the user's problem. When part or all of that problem falls outside your expertise, your job shifts from executor to orchestrator: find the right specialist, brief them clearly, verify their output, and own the final answer.

**Core principle: You retain accountability.** The specialist executes — you verify. Never hand a raw specialist output to the user without checking it against the original problem. The user trusts you, not the specialist.

### The Orchestration Loop

When delegating work outside your role, follow this cycle:

1. **Find** — Use FindAgent to locate a specialist whose expertise matches the task domain.
2. **Brief** — Use Delegate with a clear, self-contained task description. Include the original user context, the specific deliverable expected, and any constraints. A vague brief produces a vague result.
3. **Wait** — Use CollectResults to retrieve the specialist's output when it completes.
4. **Verify** — Inspect the result against the user's original problem. Ask: does this actually answer the user's question? Is it complete? Is it correct? Do not accept polished-looking output that misses the point.
5. **Score** — Use Rank to record a score for the specialist. Score based on problem resolution, not presentation:
   - 3 (excellent): The result directly and thoroughly solves the user's problem. Minimal follow-up needed.
   - 2 (good): The result addresses the core problem but needs minor clarification or filling in gaps.
   - 1 (needs improvement): The result is partially relevant but misses key aspects or contains errors.
   - 0 (poor): The result is irrelevant, wrong, or required a full redo.
6. **Report** — Present the verified result to the user honestly. If the result fully resolves the problem, deliver it. If there are gaps or errors, explain them clearly and propose the next step (retry with clarification, try a different specialist, or handle the remaining portion yourself).

### When to delegate to another agent
- The user asks for something that is not in your area of expertise (e.g. you are a code reviewer and they ask for legal advice).
- The task requires a specialized capability you do not have access to.
- The user explicitly requests that another agent handle the task.

### When to parallelize by spawning multiple agents
- The current task involves many independent sub-tasks that could run in parallel (e.g. reviewing 10 files, researching 5 topics, testing 3 configurations).
- You estimate that the total task would take significantly longer if done sequentially.
- Each sub-task is self-contained and does not depend on results from other sub-tasks.

Call Delegate multiple times in the same round for each sub-task — they run in parallel. Use CollectResults to gather all outcomes. Apply the orchestration loop (verify → score → report) to each result.

### When to create a new agent
- A specialized task type repeats frequently, and no existing agent covers it.
- The user asks you to define a new expert role with a custom system prompt.

When creating an agent, call ModelList to see available models and SkillList to see available skills. Select the model and skills that match the new agent's role.

### When NOT to delegate
- The task is within your own area of expertise — handle it yourself.
- The task is trivial and delegation overhead exceeds the benefit.
- You can answer directly from memory or with a single tool call.`
}

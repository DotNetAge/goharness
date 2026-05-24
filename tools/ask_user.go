package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DotNetAge/goreact/core"
)

// AskUser is a tool that asks the user questions via the permission system.
// Unlike the old design, this tool does NOT return an _interaction marker.
// Instead, the executor emits an AskUserRequest event with structured
// questions, the permission dialog collects answers, and answers are injected
// into params via UpdatedInput. Execute() is an identity function that formats
// the answers as a natural-language message for the LLM.
type AskUser struct {
	info *core.ToolInfo
}

// NewAskUserTool creates a new AskUser tool.
func NewAskUserTool() core.FuncTool {
	return &AskUser{
		info: &core.ToolInfo{
			Name:        "AskUser",
			Description: "Asks the user multiple choice questions to gather information, clarify ambiguity, understand preferences, make decisions or offer them choices.",
			Prompt: `Use this tool when you need to ask the user questions during execution. This allows you to:
	1. Gather user preferences or requirements
	2. Clarify ambiguous instructions
	3. Get decisions on implementation choices as you work
	4. Offer choices to the user about what direction to take.

Usage notes:
- Users will always be able to select "Other" to provide custom text input
- Use multiSelect: true to allow multiple answers to be selected for a question
- If you recommend a specific option, make that the first option in the list and add "(Recommended)" at the end of the label`,
			Tags:        []string{"interaction", "question", "clarify", "human"},
			IsReadOnly:  false,
			SecurityLevel: core.LevelSensitive,
			Parameters: []core.Parameter{
				{
					Name:        "question",
					Type:        "string",
					Description: "The clarifying question to ask the user. Be specific and concise.",
					Required:    true,
				},
				{
					Name:        "options",
					Type:        "array",
					Description: "Optional list of answer choices (2-4 items). If you recommend a specific option, add \"(Recommended)\" at the end of its label. Omit for open-ended questions.",
					Required:    false,
				},
				{
					Name:        "multiSelect",
					Type:        "boolean",
					Description: "Set to true to allow the user to select multiple options instead of just one. Use when choices are not mutually exclusive.",
					Required:    false,
				},
			},
		},
	}
}

func (t *AskUser) Info() *core.ToolInfo {
	return t.info
}

// Execute is an identity function. The actual interaction (showing question dialog,
// collecting user answer) is handled by the executor's awaitUserResponse via
// AskUserRequest event. The user's answers arrive via params["answers"].
func (t *AskUser) Execute(ctx context.Context, params map[string]any) (any, error) {
	question, ok := params["question"].(string)
	if !ok || question == "" {
		return nil, fmt.Errorf("missing required parameter: question")
	}

	// If answers are present (injected via permission UpdatedInput), format them
	if answers, ok := params["answers"].(map[string]any); ok && len(answers) > 0 {
		return formatAnswerResult(question, answers), nil
	}

	// Fallback: permission was granted without explicit answers (e.g., auto-allow)
	return fmt.Sprintf(`Asked user: "%s". Proceed based on the response.`, question), nil
}

// formatAnswerResult builds a natural-language result string for the LLM,
// matching Claude Code's approach of telling the model what to do with the answer.
func formatAnswerResult(question string, answers map[string]any) string {
	// Sort keys for deterministic output
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, q := range keys {
		parts = append(parts, fmt.Sprintf(`"%s" = "%v"`, q, answers[q]))
	}
	return fmt.Sprintf(
		"User has answered your questions: %s. You can now continue with the user's answers in mind.",
		strings.Join(parts, ", "),
	)
}

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/DotNetAge/goreact/memory"
)

// MemorySearch implements a tool for searching long-term and session memory.
// It provides semantic retrieval over stored knowledge using the memory.Memory interface.
type MemorySearch struct {
	memory memory.Memory
}

// NewMemorySearch creates a MemorySearch tool with the given Memory implementation.
// Returns nil if memory is nil (memory not configured).
func NewMemorySearch(memory memory.Memory) FuncTool {
	if memory == nil {
		return nil
	}
	return &MemorySearch{memory: memory}
}

func (t *MemorySearch) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "MemorySearch",
		MaxResultSizeChars: 30000,
		Description:        "Search long-term memory for relevant past knowledge, experiences, or data. Use this when you need information from previous interactions, user preferences, historical context, or domain-specific knowledge that may have been stored in memory.",
		Prompt: `Search long-term memory for relevant past knowledge, experiences, or data.
Use this when you need information from previous interactions, user preferences, historical context, or domain-specific knowledge.
This should be your FIRST source of external information before searching the internet.`,
		Tags:       []string{"memory", "knowledge", "search", "retrieval"},
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "Semantic search query to find relevant memories. Be specific and use natural language.",
				Required:    true,
			},
			{
				Name:        "limit",
				Type:        "integer",
				Description: "Maximum number of memory records to return (default: 5, max: 20).",
				Required:    false,
			},
			{
				Name:        "types",
				Type:        "array",
				Description: "Filter by memory type: [\"longterm\"] for persistent knowledge, [\"session\"] for current session only. Default searches both.",
				Required:    false,
			},
		},
	}
}

func (t *MemorySearch) Execute(ctx context.Context, params map[string]any) (any, error) {
	query, err := ValidateRequiredString(params, "query")
	if err != nil {
		return nil, err
	}

	query = strings.TrimSpace(query)
	if len(query) < 2 {
		return nil, fmt.Errorf("query must be at least 2 characters")
	}

	limit := 5
	if raw, ok := params["limit"]; ok {
		if v, ok := ToFloat64(raw); ok && v > 0 {
			limit = int(v)
			if limit > 20 {
				limit = 20
			}
		}
	}

	var opts []memory.RetrieveOption
	opts = append(opts, memory.WithMemoryLimit(limit))

	if typesRaw, ok := params["types"].([]any); ok && len(typesRaw) > 0 {
		var memTypes []memory.MemoryType
		for _, t := range typesRaw {
			if typeStr, ok := t.(string); ok {
				switch strings.ToLower(typeStr) {
				case "longterm", "long-term", "long_term":
					memTypes = append(memTypes, memory.MemoryTypeLongTerm)
				case "session":
					memTypes = append(memTypes, memory.MemoryTypeSession)
				}
			}
		}
		if len(memTypes) > 0 {
			opts = append(opts, memory.WithMemoryTypes(memTypes...))
		}
	}

	logger := getLogger(ctx)
	logger.Info("searching memory",
		"query", truncateStr(query, 100),
		"limit", limit,
	)

	records, err := t.memory.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("memory search failed: %w", err)
	}

	if len(records) == 0 {
		return fmt.Sprintf("No memories found for query: %q\n\nThe memory is empty or no relevant information was found. Try rephrasing your query or search the internet instead.", query), nil
	}

	if len(records) > limit {
		records = records[:limit]
	}

	result := formatMemorySearchResults(query, records)
	logger.Info("memory search completed",
		"query", truncateStr(query, 100),
		"result_count", len(records),
	)

	return result, nil
}

func formatMemorySearchResults(query string, records []memory.MemoryRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Memory search results for query: %q\n\n", query)
	fmt.Fprintf(&sb, "Found %d relevant memory record(s):\n\n", len(records))

	for i, r := range records {
		fmt.Fprintf(&sb, "--- Record %d ---\n", i+1)

		if r.ID != "" {
			fmt.Fprintf(&sb, "ID: %s\n", r.ID)
		}

		typeLabel := memoryTypeLabel(r.Type)
		fmt.Fprintf(&sb, "Type: %s\n", typeLabel)

		if r.Title != "" {
			fmt.Fprintf(&sb, "Title: %s\n", r.Title)
		}

		if r.Score > 0 {
			fmt.Fprintf(&sb, "Relevance: %.2f\n", r.Score)
		}

		if len(r.Tags) > 0 {
			fmt.Fprintf(&sb, "Tags: [%s]\n", strings.Join(r.Tags, ", "))
		}

		fmt.Fprintf(&sb, "\n%s\n", r.Content)

		if i < len(records)-1 {
			fmt.Fprintln(&sb)
		}
	}

	return sb.String()
}

func memoryTypeLabel(t memory.MemoryType) string {
	switch t {
	case memory.MemoryTypeSession:
		return "Session Memory"
	case memory.MemoryTypeLongTerm:
		return "Long-term Knowledge"
	default:
		return "Unknown"
	}
}

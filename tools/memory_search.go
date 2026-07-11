package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/DotNetAge/goharness/memory"
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
		Description:        "搜索长期记忆以获取相关的过往知识、经验或数据。",
		Prompt: `搜索长期记忆以获取相关的过往知识、经验或数据。
当您需要来自先前交互、用户偏好、历史上下文或特定领域知识的信息时，请使用此工具。
这应该是您在搜索互联网之前的第一个外部信息来源。`,
		Tags:       []string{"memory", "knowledge", "search", "retrieval"},
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "用于查找相关记忆的语义搜索查询。请具体并使用自然语言。",
				Required:    true,
			},
			{
				Name:        "limit",
				Type:        "integer",
				Description: "要返回的最大记忆记录数（默认：5，最大：20）。",
				Required:    false,
			},
			{
				Name:        "types",
				Type:        "array",
				Description: "按记忆类型过滤：[\"longterm\"] 表示持久知识，[\"session\"] 仅表示当前会话。默认搜索两者。",
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
		return nil, fmt.Errorf("查询必须至少为 2 个字符")
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

	chunks, err := t.memory.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("记忆搜索失败：%w", err)
	}

	if len(chunks) == 0 {
		return fmt.Sprintf("未找到关于查询的记忆：%q\n\n记忆为空或未找到相关信息。请尝试换一种方式表述您的查询，或搜索互联网。", query), nil
	}

	if len(chunks) > limit {
		chunks = chunks[:limit]
	}

	result := formatMemorySearchResults(query, chunks)
	logger.Info("memory search completed",
		"query", truncateStr(query, 100),
		"result_count", len(chunks),
	)

	return result, nil
}

func formatMemorySearchResults(query string, chunks []memory.MemoryChunk) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "查询的记忆搜索结果：%q\n\n", query)
	fmt.Fprintf(&sb, "找到 %d 条相关记忆记录：\n\n", len(chunks))

	for i, c := range chunks {
		fmt.Fprintf(&sb, "--- 记录 %d ---\n", i+1)

		if c.ID != "" {
			fmt.Fprintf(&sb, "ID：%s\n", c.ID)
		}
		if c.Summary != "" {
			fmt.Fprintf(&sb, "摘要：%s\n", c.Summary)
		}
		if c.AgentName != "" {
			fmt.Fprintf(&sb, "代理：%s\n", c.AgentName)
		}
		if len(c.Tags) > 0 {
			fmt.Fprintf(&sb, "标签：[%s]\n", strings.Join(c.Tags, ", "))
		}
		if !c.Timestamp.IsZero() {
			fmt.Fprintf(&sb, "时间：%s\n", c.Timestamp.Format("2006-01-02 15:04:05"))
		}

		fmt.Fprintf(&sb, "\n%s\n", c.Content)

		if i < len(chunks)-1 {
			fmt.Fprintln(&sb)
		}
	}

	return sb.String()
}

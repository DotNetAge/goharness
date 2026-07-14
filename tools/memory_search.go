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
		Description:        "回忆过往会话中的知识、决策或经历。",
		Prompt: `回忆过往会话中的知识、决策或经历。
当用户表达回忆意图（例如"回忆"、"你还记得"、"我上次说的"、"之前我们讨论的"），或需要来自先前交互、用户偏好、历史上下文、特定领域知识的信息时，请使用此工具。
这应该是您在搜索互联网之前的第一个外部信息来源。`,
		Tags:       []string{"memory", "knowledge", "recall", "retrieval"},
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
				Name:        "project_dir",
				Type:        "string",
				Description: "限定只搜索指定项目目录下的记忆。不传则搜索当前 Agent 下所有会话的记忆。",
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

	var baseOpts []memory.RetrieveOption
	baseOpts = append(baseOpts, memory.WithMemoryLimit(limit))

	// 始终按当前 Agent 过滤
	if tc := GetToolContext(ctx); tc != nil {
		if tc.Session != nil {
			baseOpts = append(baseOpts, memory.WithAgentName(tc.Session.AgentName()))
		}
	}

	// project_dir 不为空则限定到指定项目目录（即当前会话范围）
	if raw, ok := params["project_dir"]; ok {
		if dir, ok := raw.(string); ok && dir != "" {
			baseOpts = append(baseOpts, memory.WithProjectDir(dir))
		}
	}

	// 将查询按空白符拆分为多个关键词，分别检索后合并去重。
	// LLM 倾向于输入空格分隔的关键词而非自然语句（如 "redis 迁移 配置"），
	// 多次查询比单次全文检索能召回更全面的结果。
	tokens := splitQueryTokens(query)

	logger := getLogger(ctx)
	logger.Info("[MemorySearch]记忆搜索",
		"query", truncateStr(query, 100),
		"tokens", tokens,
		"limit", limit,
	)

	var all []memory.MemoryChunk
	seen := make(map[string]bool)
	for _, token := range tokens {
		chunks, err := t.memory.Retrieve(ctx, token, baseOpts...)
		if err != nil {
			logger.Warn("[MemorySearch]子查询失败，跳过", "token", token, "error", err)
			continue
		}
		for _, c := range chunks {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			all = append(all, c)
		}
	}

	if len(all) == 0 {
		return fmt.Sprintf("未找到关于查询的记忆：%q\n\n记忆为空或未找到相关信息。请尝试换一种方式表述您的查询，或搜索互联网。", query), nil
	}

	if len(all) > limit {
		all = all[:limit]
	}

	result := formatMemorySearchResults(query, all)
	logger.Info("memory search completed",
		"query", truncateStr(query, 100),
		"result_count", len(all),
	)

	return result, nil
}

// splitQueryTokens 将查询拆分为多个关键词。
// 如果查询本身是自然语句（含空格但长度 > 50），视为完整查询不拆分。
// 否则按空白符拆分为多个关键词，过滤掉过短的词。
func splitQueryTokens(query string) []string {
	// 长文本视为自然语句，不拆分
	if len(query) > 50 {
		return []string{query}
	}

	parts := strings.Fields(query)
	if len(parts) <= 1 {
		return []string{query}
	}

	// 过滤掉过短的词（<=1 字符）
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 2 {
			tokens = append(tokens, p)
		}
	}
	if len(tokens) == 0 {
		return []string{query}
	}
	return tokens
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
		// if c.AgentName != "" {
		// 	fmt.Fprintf(&sb, "代理：%s\n", c.AgentName)
		// }
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

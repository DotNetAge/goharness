package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/logging"
	"gopkg.in/yaml.v3"
)

// LoadAgentsFrom 从指定目录加载所有 Agent 配置文件，并返回初始化好的 AgentRegistry。
//
// 该函数执行以下操作：
//  1. 将目录路径转换为绝对路径
//  2. 应用可选的 AgentRegistryOption 配置选项（如自定义 Logger）
//  3. 扫描目录中的所有 .md 文件（不区分大小写）
//  4. 解析每个 Markdown 文件，提取 YAML frontmatter 和正文内容
//  5. 将解析成功的 Agent 配置注册到 AgentRegistry 中
//
// 对于解析失败的文件，函数会记录警告日志并跳过，不会中断整体加载过程。
// 这允许部分损坏的配置文件不影响其他正常文件的加载。
//
// 参数 dir 可以是相对路径或绝对路径。opts 参数用于自定义 Registry 行为，
// 例如使用 WithLogger 选项指定自定义的日志记录器。
func LoadAgentsFrom(dir string, opts ...AgentRegistryOption) (*AgentRegistry, error) {
	absPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	cfg := &agentRegistryOption{logger: logging.DefaultLogger()}
	for _, opt := range opts {
		opt(cfg)
	}

	registry := &AgentRegistry{
		path:   absPath,
		agents: make(map[string]*AgentConfig),
		logger: cfg.logger,
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", absPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			filePath := filepath.Join(absPath, entry.Name())
			agent, err := parseAgentFile(filePath)
			if err != nil {
				registry.logger.Warn("failed to parse agent file, skipping",
					"path", filePath,
					"error", err)
				continue
			}
			registry.agents[agent.Name] = agent
		}
	}
	return registry, nil
}

// parseAgentFile 解析单个 Agent 配置文件，返回解析后的 AgentConfig 对象。
//
// 文件格式要求：
//   - 必须以 "---" 开头（YAML frontmatter 开始标记）
//   - 必须包含两个 "---" 分隔符（frontmatter 开始和结束）
//   - frontmatter 部分必须是有效的 YAML 格式
//   - 两个分隔符之间的内容为 YAML 元数据
//   - 第二个分隔符之后的内容为正文（Introduction/Body）
//
// 支持的字段：
//   - name (必需): Agent 名称
//   - role 或 title: Agent 角色（优先使用 role，回退到 title）
//   - description: 描述信息
//   - model: 默认模型名称
//   - skills: 技能列表（数组）
//   - exclude_tools: 要排除的工具列表（数组）
//   - meta: 扩展元数据（支持 map 或数组格式）
//
// 该函数会处理 Windows 换行符（\r\n）并自动转换为 Unix 格式（\n）。
func parseAgentFile(filePath string) (*AgentConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.TrimLeft(content, "\n\r\t ")

	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("invalid agent file format, missing frontmatter delimiter")
	}
	lines := strings.Split(content, "\n")
	var frontmatterLines []string
	var bodyLines []string
	delimCount := 0
	inBody := false
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			delimCount++
			if delimCount == 2 {
				inBody = true
				continue
			}
			continue
		}
		if i == 0 {
			continue
		}
		if !inBody {
			frontmatterLines = append(frontmatterLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	if delimCount < 2 {
		return nil, fmt.Errorf("invalid agent file format, missing closing frontmatter delimiter")
	}
	frontmatterYAML := strings.Join(frontmatterLines, "\n")
	body := strings.Join(bodyLines, "\n")
	body = strings.TrimSpace(body)

	var meta map[string]any
	if err := yaml.Unmarshal([]byte(frontmatterYAML), &meta); err != nil {
		return nil, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	agent := &AgentConfig{}
	if nameVal, ok := meta["name"].(string); ok {
		agent.Name = nameVal
	}
	if roleVal, ok := meta["role"].(string); ok {
		agent.Role = roleVal
	} else if titleVal, ok := meta["title"].(string); ok {
		agent.Role = titleVal
	}
	if descVal, ok := meta["description"].(string); ok {
		agent.Description = descVal
	}
	if modelVal, ok := meta["model"].(string); ok {
		agent.Model = modelVal
	}
	if skillsList, ok := meta["skills"].([]any); ok {
		for _, s := range skillsList {
			if str, ok := s.(string); ok {
				agent.Skills = append(agent.Skills, str)
			}
		}
	}
	if excludeList, ok := meta["exclude_tools"].([]any); ok {
		for _, e := range excludeList {
			if str, ok := e.(string); ok {
				agent.ExcludeTools = append(agent.ExcludeTools, str)
			}
		}
	}
	if metaVal, ok := meta["meta"]; ok {
		if metaMap, ok := metaVal.(map[string]any); ok {
			agent.Meta = deepCopyMeta(metaMap)
		} else if metaSlice, ok := metaVal.([]any); ok {
			agent.Meta = make(map[string]any)
			for _, item := range metaSlice {
				if itemMap, ok := item.(map[string]any); ok {
					for k, v := range itemMap {
						agent.Meta[k] = v
					}
				}
			}
			if len(agent.Meta) == 0 {
				agent.Meta = nil
			}
		}
	}

	agent.Introduction = body
	return agent, nil
}

// deepCopyMeta 递归地深拷贝元数据映射，确保修改副本不会影响原始数据。
// 支持嵌套的 map[string]any 和 []any 类型的递归拷贝。
func deepCopyMeta(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		switch val := v.(type) {
		case map[string]any:
			dst[k] = deepCopyMeta(val)
		case []any:
			newSlice := make([]any, len(val))
			for i, item := range val {
				if m, ok := item.(map[string]any); ok {
					newSlice[i] = deepCopyMeta(m)
				} else {
					newSlice[i] = item
				}
			}
			dst[k] = newSlice
		default:
			dst[k] = v
		}
	}
	return dst
}

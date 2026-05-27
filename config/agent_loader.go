package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goreact/logging"
	"gopkg.in/yaml.v3"
)

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

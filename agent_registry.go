package goreact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goreact/core"
	"gopkg.in/yaml.v3"
)

// AgentRegistry holds parsed agent configurations indexed by name.
type AgentRegistry struct {
	path   string
	agents map[string]*core.AgentConfig
	logger core.Logger
}

// agentRegistryOption holds configuration options for LoadAgentsFrom.
type agentRegistryOption struct {
	logger core.Logger
}

// AgentRegistryOption is a function that configures agent registry loading.
type AgentRegistryOption func(*agentRegistryOption)

// WithRegistryLogger returns an Option that sets the logger for agent registry operations.
func WithRegistryLogger(logger core.Logger) AgentRegistryOption {
	return func(o *agentRegistryOption) { o.logger = logger }
}

// LoadFrom loads all agent definition files (.md) from the specified directory,
// parses their YAML frontmatter and system prompt body, and returns an AgentRegistry.
//
// Options:
//   - WithRegistryLogger: custom logger for parsing warnings (defaults to core.DefaultLogger)
func LoadAgentsFrom(dir string, opts ...AgentRegistryOption) (*AgentRegistry, error) {
	absPath, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	cfg := &agentRegistryOption{logger: core.DefaultLogger()}
	for _, opt := range opts {
		opt(cfg)
	}

	registry := &AgentRegistry{
		path:   absPath,
		agents: make(map[string]*core.AgentConfig),
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

// parseAgentFile reads a markdown file, extracts YAML frontmatter and body.
func parseAgentFile(filePath string) (*core.AgentConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	// normalize line endings to \n
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.TrimLeft(content, "\n\r\t ")

	// expect first line to be "---"
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("invalid agent file format, missing frontmatter delimiter")
	}
	// find the next "---" line after the first
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
			continue // skip first delimiter line
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

	// parse YAML frontmatter into a map first
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(frontmatterYAML), &meta); err != nil {
		return nil, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	agent := &core.AgentConfig{}
	// map fields
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

func (r *AgentRegistry) Get(name string) *core.AgentConfig {
	return r.agents[name]
}

func (r *AgentRegistry) List() []*core.AgentConfig {
	var agents []*core.AgentConfig
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	return agents
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

// It does not store the config in the registry.
func (r *AgentRegistry) Read(file string) (*core.AgentConfig, error) {
	absPath := filepath.Join(r.path, file)
	return parseAgentFile(absPath)
}

// Remove removes an agent from the registry and deletes its markdown file.
func (r *AgentRegistry) Remove(name string) error {
	_, exists := r.agents[name]
	if !exists {
		return fmt.Errorf("agent %s not found", name)
	}
	fileName := strings.ToLower(name) + ".md"
	filePath := filepath.Join(r.path, fileName)
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("failed to delete file %s: %w", filePath, err)
	}
	delete(r.agents, name)
	return nil
}

// SaveTo saves an agent config as a markdown file in the registry directory.
func (r *AgentRegistry) SaveTo(agent *core.AgentConfig) error {
	if agent.Name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	fileName := strings.ToLower(agent.Name) + ".md"
	filePath := filepath.Join(r.path, fileName)

	meta := make(map[string]any)
	meta["name"] = agent.Name
	if agent.Role != "" {
		meta["role"] = agent.Role
	}
	if agent.Description != "" {
		meta["description"] = agent.Description
	}
	if agent.Model != "" {
		meta["model"] = agent.Model
	}
	if len(agent.Skills) > 0 {
		meta["skills"] = agent.Skills
	}
	if agent.EnableOrchestration {
		meta["enable_orchestration"] = agent.EnableOrchestration
	}
	if agent.MaxDecomposeDepth > 0 {
		meta["max_decompose_depth"] = agent.MaxDecomposeDepth
	}
	if len(agent.Meta) > 0 {
		meta["meta"] = agent.Meta
	}

	yamlData, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML frontmatter: %w", err)
	}

	body := agent.Body
	if body == "" {
		body = agent.Introduction
	}
	content := fmt.Sprintf("---\n%s---\n%s", string(yamlData), body)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", filePath, err)
	}
	r.agents[agent.Name] = agent
	return nil
}

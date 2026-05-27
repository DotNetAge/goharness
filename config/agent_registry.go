package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DotNetAge/goreact/logging"
	"gopkg.in/yaml.v3"
)

type AgentRegistry struct {
	mu     sync.RWMutex
	path   string
	agents map[string]*AgentConfig
	logger logging.Logger
}

func (r *AgentRegistry) Get(name string) *AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[name]
}

func (r *AgentRegistry) List() []*AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var agents []*AgentConfig
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	return agents
}

func (r *AgentRegistry) Read(file string) (*AgentConfig, error) {
	r.mu.RLock()
	path := r.path
	r.mu.RUnlock()
	absPath := filepath.Join(path, file)
	return parseAgentFile(absPath)
}

func (r *AgentRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *AgentRegistry) SaveTo(agent *AgentConfig) error {
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
	r.mu.Lock()
	r.agents[agent.Name] = agent
	r.mu.Unlock()
	return nil
}

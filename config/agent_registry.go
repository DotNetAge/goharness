package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/logging"
	"gopkg.in/yaml.v3"
)

// AgentRegistry 提供了 Agent 配置的注册、查询和持久化管理功能。
// 它维护了一个线程安全的内存映射，将 Agent 名称映射到对应的配置对象，
// 同时支持从文件系统加载和保存 Agent 配置。
//
// AgentRegistry 采用读写锁（sync.RWMutex）保证并发安全，
// 支持多 goroutine 同时读取，但写操作会独占访问权限。
// Agent 配置以 Markdown 文件（带 YAML frontmatter）的形式
// 存储在指定的目录中，文件名格式为 "{name}.md"（小写）。
type AgentRegistry struct {
	mu     sync.RWMutex
	path   string
	agents map[string]*AgentConfig
	logger logging.Logger
}

// Get 根据名称查找并返回已注册的 Agent 配置。
// 如果未找到对应名称的 Agent，返回 nil。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (r *AgentRegistry) Get(name string) *AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[name]
}

// List 返回所有已注册的 Agent 配置列表。
// 返回的切片是新生成的，修改不会影响内部存储。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (r *AgentRegistry) List() []*AgentConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var agents []*AgentConfig
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	return agents
}

// Read 从指定文件名读取并解析单个 Agent 配置文件。
// 文件路径相对于 AgentRegistry 的根目录（path 字段）。
//
// 与 Get 方法不同，Read 每次都会重新从磁盘读取文件，
// 适用于需要获取最新文件内容的场景。如果文件解析失败，
// 返回相应的错误信息。该方法不会更新内存中的缓存。
func (r *AgentRegistry) Read(file string) (*AgentConfig, error) {
	r.mu.RLock()
	path := r.path
	r.mu.RUnlock()
	absPath := filepath.Join(path, file)
	return parseAgentFile(absPath)
}

// Remove 从注册表中删除指定名称的 Agent，并同时删除其对应的配置文件。
// 如果 Agent 不存在于注册表中，返回错误。
//
// 删除操作是原子性的：只有当文件删除成功后，才会从内存中移除该 Agent。
// 该方法会获取写锁以保证操作的原子性和线程安全。
func (r *AgentRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.agents[name]
	if !exists {
		return fmt.Errorf("未找到智能体 %s", name)
	}
	fileName := strings.ToLower(name) + ".md"
	filePath := filepath.Join(r.path, fileName)
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("无法删除文件 %s: %w", filePath, err)
	}
	delete(r.agents, name)
	return nil
}

// SaveTo 将 Agent 配置持久化到文件系统，并更新内存中的注册表。
// 配置文件以 Markdown 格式存储，包含 YAML frontmatter 和正文内容。
//
// 保存流程：
//  1. 验证 Agent 名称不为空
//  2. 生成文件名（小写的 name + ".md"）
//  3. 构造 YAML frontmatter（只包含非空字段）
//  4. 组合 frontmatter 和 body 内容写入文件
//  5. 更新内存中的 agents 映射
//
// 如果 Body 为空，则使用 Introduction 作为正文内容。
// 该方法会获取写锁以保证操作的原子性。
func (r *AgentRegistry) SaveTo(agent *AgentConfig) error {
	if agent.Name == "" {
		return fmt.Errorf("智能体名称不能为空")
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
		return fmt.Errorf("无法序列化 YAML frontmatter: %w", err)
	}

	content := fmt.Sprintf("---\n%s---\n%s", string(yamlData), agent.Introduction)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("无法写入文件 %s: %w", filePath, err)
	}
	r.mu.Lock()
	r.agents[agent.Name] = agent
	r.mu.Unlock()
	return nil
}

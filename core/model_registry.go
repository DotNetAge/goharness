package core

import (
	"fmt"
	"sync"
)

// ModelRegistry manages multiple LLM backend configurations.
// It allows different agents to use different models based on their
// AgentConfig.Model field (which serves as a lookup key).
//
// Usage:
//
//	reg := NewInMemoryModelRegistry()
//	reg.Register("deepseek-chat", &ModelConfig{...})
//	reg.Register("qwen3.5-flash", &ModelConfig{...})
//
//	cfg, err := reg.Get("deepseek-chat") // lookup by name
type ModelRegistry interface {
	// Get retrieves a model configuration by name (lookup key).
	// Returns ErrModelNotFound if the name is not registered.
	Get(name string) (*ModelConfig, error)

	// Register adds a new model configuration under the given name.
	// Returns ErrDuplicateModel if the name already exists.
	Register(name string, config *ModelConfig) error

	// List returns all registered model names.
	List() []string

	// Size returns the number of registered models.
	Size() int
}

// ModelRegistry errors.
var (
	ErrModelNotFound  = fmt.Errorf("model registry: model not found")
	ErrDuplicateModel = fmt.Errorf("model registry: duplicate model name")
)

// InMemoryModelRegistry is the default in-memory ModelRegistry implementation.
type InMemoryModelRegistry struct {
	mu     sync.RWMutex
	models map[string]*ModelConfig
}

// NewInMemoryModelRegistry creates an empty model registry.
func NewInMemoryModelRegistry() *InMemoryModelRegistry {
	return &InMemoryModelRegistry{
		models: make(map[string]*ModelConfig),
	}
}

func (m *InMemoryModelRegistry) Get(name string) (*ModelConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.models == nil {
		return nil, ErrModelNotFound
	}
	cfg, ok := m.models[name]
	if !ok {
		return nil, ErrModelNotFound
	}
	return cfg, nil
}

func (m *InMemoryModelRegistry) Register(name string, config *ModelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if name == "" {
		return fmt.Errorf("model registry: model name must not be empty")
	}
	if config == nil {
		return fmt.Errorf("model registry: model config must not be nil")
	}
	if m.models == nil {
		m.models = make(map[string]*ModelConfig)
	}
	if _, exists := m.models[name]; exists {
		return ErrDuplicateModel
	}
	m.models[name] = config
	return nil
}

func (m *InMemoryModelRegistry) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.models) == 0 {
		return nil
	}
	names := make([]string, 0, len(m.models))
	for name := range m.models {
		names = append(names, name)
	}
	return names
}

func (m *InMemoryModelRegistry) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.models == nil {
		return 0
	}
	return len(m.models)
}

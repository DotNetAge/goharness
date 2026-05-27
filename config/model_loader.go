package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type ModelRegistry struct {
	settingFile string
	models      map[string]*ModelConfig
	providers   map[string]*ProviderConfig
}

func LoadModels(path string) (*ModelRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read models file: %w", err)
	}

	var configs ModelsConfig
	if err := yaml.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal models YAML: %w", err)
	}

	reg := &ModelRegistry{
		settingFile: path,
		models:      make(map[string]*ModelConfig, len(configs.Models)),
		providers:   make(map[string]*ProviderConfig, len(configs.Providers)),
	}

	for i := range configs.Providers {
		p := &configs.Providers[i]
		if p.Name == "" {
			return nil, fmt.Errorf("provider config missing name")
		}
		reg.providers[p.Name] = p
	}

	for i := range configs.Models {
		cfg := &configs.Models[i]
		if cfg.Name == "" {
			return nil, fmt.Errorf("model config missing name")
		}
		reg.models[cfg.Name] = cfg
	}

	return reg, nil
}

func (m *ModelRegistry) resolveProvider(cfg *ModelConfig) *ModelConfig {
	if cfg == nil || cfg.Provider == "" {
		return cfg
	}
	provider, ok := m.providers[cfg.Provider]
	if !ok {
		return cfg
	}
	resolved := *cfg
	if resolved.BaseURL == "" {
		resolved.BaseURL = provider.BaseURL
	}
	if resolved.APIKey == "" {
		resolved.APIKey = provider.APIKey
	}
	if resolved.AuthToken == "" {
		resolved.AuthToken = provider.AuthToken
	}
	return &resolved
}

func (m *ModelRegistry) Get(name string) *ModelConfig {
	mc := m.models[name]
	if mc == nil {
		return nil
	}
	return m.resolveProvider(mc)
}

func (m *ModelRegistry) List() []*ModelConfig {
	var result []*ModelConfig
	for name := range m.models {
		mc := m.resolveProvider(m.models[name])
		result = append(result, mc)
	}
	return result
}

func (m *ModelRegistry) GetRaw(name string) *ModelConfig {
	return m.models[name]
}

func (m *ModelRegistry) ListRaw() []*ModelConfig {
	var result []*ModelConfig
	for _, mc := range m.models {
		result = append(result, mc)
	}
	return result
}

func (m *ModelRegistry) Register(name string, cfg *ModelConfig) {
	if m.models == nil {
		m.models = make(map[string]*ModelConfig)
	}
	if cfg.Name == "" {
		cfg.Name = name
	}
	m.models[name] = cfg
}

func (m *ModelRegistry) Save(cfg *ModelConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("model name cannot be empty")
	}

	if m.models == nil {
		m.models = make(map[string]*ModelConfig)
	}
	m.models[cfg.Name] = cfg

	return m.saveAll()
}

func (m *ModelRegistry) saveAll() error {
	configs := make([]ModelConfig, 0, len(m.models))
	for _, cfg := range m.models {
		if cfg == nil {
			continue
		}
		configs = append(configs, *cfg)
	}

	providersList := make([]ProviderConfig, 0, len(m.providers))
	for _, p := range m.providers {
		if p == nil {
			continue
		}
		providersList = append(providersList, *p)
	}

	wrapper := ModelsConfig{
		Models: configs,
	}
	if len(providersList) > 0 {
		wrapper.Providers = providersList
	}

	data, err := yaml.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("failed to marshal models: %w", err)
	}

	if err := os.WriteFile(m.settingFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write models file: %w", err)
	}
	return nil
}

func (m *ModelRegistry) Providers() []*ProviderConfig {
	var result []*ProviderConfig
	for _, p := range m.providers {
		result = append(result, p)
	}
	return result
}

func (m *ModelRegistry) GetProvider(name string) *ProviderConfig {
	return m.providers[name]
}

func (m *ModelRegistry) RegisterProvider(name string, provider *ProviderConfig) {
	if m.providers == nil {
		m.providers = make(map[string]*ProviderConfig)
	}
	if provider.Name == "" {
		provider.Name = name
	}
	m.providers[name] = provider
}

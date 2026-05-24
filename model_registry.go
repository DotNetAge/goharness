package goreact

import (
	"fmt"
	"os"

	"github.com/DotNetAge/goreact/core"
	"gopkg.in/yaml.v3"
)

type ModelsConfig struct {
	Providers []core.ProviderConfig `yaml:"providers"`
	Models    []core.ModelConfig    `yaml:"models"`
}

type ModelRegistry struct {
	settingFile string
	models      map[string]*core.ModelConfig
	providers   map[string]*core.ProviderConfig
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
		models:      make(map[string]*core.ModelConfig, len(configs.Models)),
		providers:   make(map[string]*core.ProviderConfig, len(configs.Providers)),
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
		// Provider validation is lenient: providers may be loaded from
		// a separate file (e.g. provider.yml) and registered later.
		reg.models[cfg.Name] = cfg
	}

	return reg, nil
}

func (m *ModelRegistry) resolveProvider(model *core.ModelConfig) *core.ModelConfig {
	if model == nil || model.Provider == "" {
		return model
	}
	provider, ok := m.providers[model.Provider]
	if !ok {
		return model
	}
	resolved := *model
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

func (m *ModelRegistry) Get(name string) *core.ModelConfig {
	model := m.models[name]
	if model == nil {
		return nil
	}
	return m.resolveProvider(model)
}

func (m *ModelRegistry) List() []*core.ModelConfig {
	var result []*core.ModelConfig
	for name := range m.models {
		model := m.resolveProvider(m.models[name])
		result = append(result, model)
	}
	return result
}

func (m *ModelRegistry) GetRaw(name string) *core.ModelConfig {
	return m.models[name]
}

func (m *ModelRegistry) ListRaw() []*core.ModelConfig {
	var result []*core.ModelConfig
	for _, model := range m.models {
		result = append(result, model)
	}
	return result
}

func (m *ModelRegistry) Register(name string, model *core.ModelConfig) {
	if m.models == nil {
		m.models = make(map[string]*core.ModelConfig)
	}
	if model.Name == "" {
		model.Name = name
	}
	m.models[name] = model
}

func (m *ModelRegistry) Save(model *core.ModelConfig) error {
	if model.Name == "" {
		return fmt.Errorf("model name cannot be empty")
	}

	if m.models == nil {
		m.models = make(map[string]*core.ModelConfig)
	}
	m.models[model.Name] = model

	return m.saveAll()
}

func (m *ModelRegistry) saveAll() error {
	configs := make([]core.ModelConfig, 0, len(m.models))
	for _, cfg := range m.models {
		if cfg == nil {
			continue
		}
		configs = append(configs, *cfg)
	}

	providersList := make([]core.ProviderConfig, 0, len(m.providers))
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

func (m *ModelRegistry) Providers() []*core.ProviderConfig {
	var result []*core.ProviderConfig
	for _, p := range m.providers {
		result = append(result, p)
	}
	return result
}

func (m *ModelRegistry) GetProvider(name string) *core.ProviderConfig {
	return m.providers[name]
}

func (m *ModelRegistry) RegisterProvider(name string, provider *core.ProviderConfig) {
	if m.providers == nil {
		m.providers = make(map[string]*core.ProviderConfig)
	}
	if provider.Name == "" {
		provider.Name = name
	}
	m.providers[name] = provider
}

type modelRegistryProviders struct {
	providers map[string]*core.ProviderConfig
}

func (r *modelRegistryProviders) Get(name string) (*core.ProviderConfig, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, core.ErrProviderNotFound
	}
	return p, nil
}

func (r *modelRegistryProviders) Register(name string, provider *core.ProviderConfig) error {
	return fmt.Errorf("provider registry: read-only adapter from ModelRegistry")
}

func (r *modelRegistryProviders) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

func (r *modelRegistryProviders) Size() int {
	return len(r.providers)
}

func (m *ModelRegistry) ProviderRegistry() core.ProviderRegistry {
	if m.providers == nil {
		m.providers = make(map[string]*core.ProviderConfig)
	}
	return &modelRegistryProviders{providers: m.providers}
}

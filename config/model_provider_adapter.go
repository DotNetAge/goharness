package config

import "fmt"

type modelRegistryProviders struct {
	providers map[string]*ProviderConfig
}

func (r *modelRegistryProviders) Get(name string) (*ProviderConfig, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return p, nil
}

func (r *modelRegistryProviders) Register(name string, provider *ProviderConfig) error {
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

func (m *ModelRegistry) ProviderRegistry() ProviderRegistry {
	if m.providers == nil {
		m.providers = make(map[string]*ProviderConfig)
	}
	return &modelRegistryProviders{providers: m.providers}
}

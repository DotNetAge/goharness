package core

import "fmt"

type ProviderConfig struct {
	Name      string `json:"name" yaml:"name"`
	Title     string `json:"title,omitempty" yaml:"title,omitempty"`
	BaseURL   string `json:"base_url" yaml:"base_url"`
	APIKey    string `json:"api_key" yaml:"api_key"`
	AuthToken string `json:"auth_token" yaml:"auth_token"`
}

type ProviderRegistry interface {
	Get(name string) (*ProviderConfig, error)
	Register(name string, provider *ProviderConfig) error
	List() []string
	Size() int
}

var (
	ErrProviderNotFound  = fmt.Errorf("provider registry: provider not found")
	ErrDuplicateProvider = fmt.Errorf("provider registry: duplicate provider name")
)

type InMemoryProviderRegistry struct {
	providers map[string]*ProviderConfig
}

func NewInMemoryProviderRegistry() *InMemoryProviderRegistry {
	return &InMemoryProviderRegistry{
		providers: make(map[string]*ProviderConfig),
	}
}

func (r *InMemoryProviderRegistry) Get(name string) (*ProviderConfig, error) {
	if r.providers == nil {
		return nil, ErrProviderNotFound
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return p, nil
}

func (r *InMemoryProviderRegistry) Register(name string, provider *ProviderConfig) error {
	if name == "" {
		return fmt.Errorf("provider registry: provider name must not be empty")
	}
	if provider == nil {
		return fmt.Errorf("provider registry: provider config must not be nil")
	}
	if r.providers == nil {
		r.providers = make(map[string]*ProviderConfig)
	}
	if _, exists := r.providers[name]; exists {
		return ErrDuplicateProvider
	}
	r.providers[name] = provider
	return nil
}

func (r *InMemoryProviderRegistry) List() []string {
	if len(r.providers) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

func (r *InMemoryProviderRegistry) Size() int {
	if r.providers == nil {
		return 0
	}
	return len(r.providers)
}

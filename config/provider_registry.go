package config

import "fmt"

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

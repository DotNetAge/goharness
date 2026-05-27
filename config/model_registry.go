package config

import "fmt"

// ModelStore is a read-only interface for model lookup.
type ModelStore interface {
	Get(name string) (*ModelConfig, error)
	List() []string
	Size() int
}

var (
	ErrModelNotFound  = fmt.Errorf("model registry: model not found")
	ErrDuplicateModel = fmt.Errorf("model registry: duplicate model name")
)

package config

import "fmt"

// ModelStore is a read-only interface for model lookup.
type ModelStore interface {
	Get(name string) (*ModelConfig, error)
	List() []string
	Size() int
}

var (
	ErrModelNotFound  = fmt.Errorf("模型注册表: 未找到模型")
	ErrDuplicateModel = fmt.Errorf("模型注册表: 模型名称重复")
)

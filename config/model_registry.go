package config

import "fmt"

// ModelStore 是用于模型查找的只读接口。
type ModelStore interface {
	Get(name string) (*ModelConfig, error)
	List() []string
	Size() int
}

var (
	ErrModelNotFound  = fmt.Errorf("模型注册表: 未找到模型")
	ErrDuplicateModel = fmt.Errorf("模型注册表: 模型名称重复")
)

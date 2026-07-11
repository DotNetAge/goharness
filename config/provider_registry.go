package config

import "fmt"

type ProviderRegistry interface {
	Get(name string) (*ProviderConfig, error)
	Register(name string, provider *ProviderConfig) error
	List() []string
	Size() int
}

var (
	ErrProviderNotFound  = fmt.Errorf("提供商注册表: 未找到提供商")
	ErrDuplicateProvider = fmt.Errorf("提供商注册表: 提供商名称重复")
)

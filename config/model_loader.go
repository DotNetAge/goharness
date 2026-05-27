package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// ModelRegistry 提供了模型和提供商配置的注册、查询和管理功能。
// 它维护了两个线程安全的内存映射：一个用于模型配置，另一个用于提供商配置。
//
// ModelRegistry 支持从 YAML 配置文件加载配置，并提供 Provider 解析功能，
// 允许模型配置继承提供商的连接参数（BaseURL、APIKey、AuthToken）。
// 所有公开方法都通过读写锁保证并发安全。
type ModelRegistry struct {
	mu          sync.RWMutex
	settingFile string
	models      map[string]*ModelConfig
	providers   map[string]*ProviderConfig
}

// LoadModels 从指定的 YAML 文件路径加载模型和提供商配置，并返回初始化好的 ModelRegistry。
//
// 该函数执行以下操作：
//  1. 读取并解析 YAML 配置文件
//  2. 验证所有 Provider 和 Model 都有非空的 Name 字段
//  3. 将配置分别注册到 providers 和 models 映射中
//
// 如果文件读取失败、YAML 解析失败、或任何配置项缺少名称字段，
// 函数会返回相应的错误。返回的 ModelRegistry 已完成初始化，
// 可以直接用于查询和管理模型配置。
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

// resolveProvider 根据模型配置中的 Provider 字段查找对应的提供商配置，
// 并用提供商的连接参数填充模型配置中未设置的字段。
//
// 这是 ModelRegistry 的内部辅助方法，用于实现配置继承机制。
// 如果模型没有指定 Provider 或 Provider 不存在，直接返回原始配置。
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

// Get 根据模型名称查找并返回已注册的模型配置（已解析 Provider）。
// 如果未找到对应名称的模型，返回 nil。
//
// 返回的配置已经过 Provider 解析处理，即如果模型配置中有未设置的
// 连接参数，会自动从关联的 Provider 继承。该方法是并发安全的。
func (m *ModelRegistry) Get(name string) *ModelConfig {
	m.mu.RLock()
	mc := m.models[name]
	m.mu.RUnlock()
	if mc == nil {
		return nil
	}
	return m.resolveProvider(mc)
}

// List 返回所有已注册的模型配置列表（均已解析 Provider）。
// 返回的切片是新生成的，修改不会影响内部存储。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (m *ModelRegistry) List() []*ModelConfig {
	m.mu.RLock()
	models := make([]*ModelConfig, 0, len(m.models))
	for name := range m.models {
		mc := m.resolveProvider(m.models[name])
		models = append(models, mc)
	}
	m.mu.RUnlock()
	return models
}

// GetRaw 根据模型名称查找并返回原始的模型配置（未经 Provider 解析）。
// 与 Get 方法不同，GetRaw 返回的是存储在注册表中的原始配置对象，
// 不进行任何 Provider 继承处理。
//
// 如果未找到对应名称的模型，返回 nil。该方法是并发安全的。
func (m *ModelRegistry) GetRaw(name string) *ModelConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.models[name]
}

// ListRaw 返回所有已注册的原始模型配置列表（未经 Provider 解析）。
// 与 List 方法不同，ListRaw 返回的是存储在注册表中的原始配置，
// 不进行任何 Provider 继承处理。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (m *ModelRegistry) ListRaw() []*ModelConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*ModelConfig
	for _, mc := range m.models {
		result = append(result, mc)
	}
	return result
}

// Register 将模型配置注册到 ModelRegistry 中。
// 如果配置的 Name 字段为空，则使用参数 name 作为配置名称。
//
// 该方法会覆盖同名的已有配置。该方法是并发安全的。
func (m *ModelRegistry) Register(name string, cfg *ModelConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.models == nil {
		m.models = make(map[string]*ModelConfig)
	}
	if cfg.Name == "" {
		cfg.Name = name
	}
	m.models[name] = cfg
}

// Save 将模型配置保存到内存注册表，并将完整的配置写入磁盘文件。
// 该方法会触发 saveAll 操作，将当前注册表中的所有模型和提供商配置
// 序列化为 YAML 格式后写回原始配置文件。
//
// 如果模型名称为空，返回错误。该方法是线程安全的。
func (m *ModelRegistry) Save(cfg *ModelConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("model name cannot be empty")
	}

	m.mu.Lock()
	if m.models == nil {
		m.models = make(map[string]*ModelConfig)
	}
	m.models[cfg.Name] = cfg
	m.mu.Unlock()

	return m.saveAll()
}

// saveAll 将当前注册表中的所有模型和提供商配置序列化并写入磁盘文件。
// 这是 ModelRegistry 的内部方法，用于持久化配置变更。
//
// 写入流程：
//  1. 收集所有非空的模型和提供商配置
//  2. 构造 ModelsConfig 包装结构
//  3. 序列化为 YAML 格式
//  4. 写入 settingFile 指定的文件路径
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

// Providers 返回所有已注册的提供商配置列表。
// 返回的切片是新生成的，修改不会影响内部存储。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (m *ModelRegistry) Providers() []*ProviderConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*ProviderConfig
	for _, p := range m.providers {
		result = append(result, p)
	}
	return result
}

// GetProvider 根据提供商名称查找并返回对应的 ProviderConfig。
// 如果未找到对应名称的提供商，返回 nil。
//
// 该方法是并发安全的，使用读锁保护共享数据。
func (m *ModelRegistry) GetProvider(name string) *ProviderConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providers[name]
}

// RegisterProvider 将提供商配置注册到 ModelRegistry 中。
// 如果配置的 Name 字段为空，则使用参数 name 作为配置名称。
//
// 该方法会覆盖同名的已有配置。该方法是并发安全的。
func (m *ModelRegistry) RegisterProvider(name string, provider *ProviderConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.providers == nil {
		m.providers = make(map[string]*ProviderConfig)
	}
	if provider.Name == "" {
		provider.Name = name
	}
	m.providers[name] = provider
}

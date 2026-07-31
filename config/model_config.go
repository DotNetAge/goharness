package config

import gochat "github.com/DotNetAge/gochat/core"

// ModelConfig 定义了 AI 模型的完整配置结构。
// 它包含了模型的身份信息、API 连接参数、能力标志以及生成参数。
// ModelConfig 支持从 ProviderConfig 继承未设置的连接参数，
// 实现了配置的层级管理和默认值解析。
type ModelConfig struct {
	// Name 是模型的唯一标识符，用于在 ModelRegistry 中注册和查找模型。
	Name string `json:"name" yaml:"name"`

	// Title 提供了模型的可读标题，用于 UI 展示和文档说明。
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Description 描述了模型的特点、适用场景和能力边界。
	Description string `json:"description" yaml:"description"`

	// Provider 指定了模型所属的服务提供商名称，对应 ProviderRegistry 中的提供商配置。
	Provider string `json:"provider" yaml:"provider"`

	// BaseURL 定义了模型 API 的基础 URL 地址，如果为空则从 Provider 继承。
	BaseURL string `json:"base_url" yaml:"base_url"`

	// APIKey 存储了访问模型 API 的密钥凭证，如果为空则从 Provider 继承。
	APIKey string `json:"api_key" yaml:"api_key"`

	// AuthToken 存储了额外的认证令牌，用于 OAuth 或其他认证机制，如果为空则从 Provider 继承。
	AuthToken string `json:"auth_token" yaml:"auth_token"`

	// ContextLength 定义了模型支持的最大上下文窗口大小（token 数）。
	ContextLength int64 `json:"context_length" yaml:"context_length"`

	// IsLocal 标识该模型是否为本地部署（如 Ollama、LM Studio 等），影响路由和资源管理策略。
	IsLocal bool `json:"is_local" yaml:"is_local"`

	// FuncCalling 表示模型是否支持函数调用（Function Calling / Tool Use）能力。
	FuncCalling bool `json:"func_calling" yaml:"func_calling"`

	// Structuring 表示模型是否支持结构化输出（如 JSON Schema 约束输出）能力。
	Structuring bool `json:"structuring" yaml:"structuring"`

	// WebSearching 表示模型是否内置或支持联网搜索能力。
	WebSearching bool `json:"web_searching" yaml:"web_searching"`

	// Visioning 表示模型是否支持视觉理解（图片输入），
	// 启用后允许在消息中传入 ImageUrl。
	Visioning bool `json:"visioning" yaml:"visioning"`

	// PrefixCon 控制是否启用前缀连续模式（Prefix Caching），用于优化长对话性能。
	PrefixCon bool `json:"prefix_con" yaml:"prefix_con"`

	// ContextCache 控制是否启用上下文缓存功能，减少重复计算的 token 消耗。
	ContextCache bool `json:"context_cache" yaml:"context_cache"`

	// TopP 设置了核采样（nucleus sampling）的概率阈值，取值范围 [0.0, 1.0]。
	TopP float64 `json:"top_p" yaml:"top_p"`

	// TopK 设置了 top-k 采样的候选 token 数量限制。
	TopK float64 `json:"top_k" yaml:"top_k"`

	// Temperature 控制生成文本的随机性程度，值越高输出越多样化，取值范围通常 [0.0, 2.0]。
	Temperature float64 `json:"temperature" yaml:"temperature"`

	// RepetitionPenalty 设置了重复惩罚系数，用于减少生成内容中的重复现象。
	RepetitionPenalty float64 `json:"repetition_penalty" yaml:"repetition_penalty"`

	// FrequencyPenalty 设置了频率惩罚系数，用于降低高频 token 的出现概率。
	FrequencyPenalty float64 `json:"frequency_penalty" yaml:"frequency_penalty"`

	// Enabled 控制该模型是否可用，禁用的模型将不会出现在可用模型列表中。
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MaxTurns 限制了单轮对话的最大交互次数，用于控制会话长度和成本。
	MaxTurns int `json:"max_turns" yaml:"max_turns"`

	// CostPer1MIn 是每百万输入 token 的费用（¥）。
	CostPer1MIn float64 `json:"cost_per_1m_in" yaml:"cost_per_1m_in"`

	// CostPer1MOut 是每百万输出 token 的费用（¥）。
	CostPer1MOut float64 `json:"cost_per_1m_out" yaml:"cost_per_1m_out"`
}

// Config 将 ModelConfig 转换为 gochat.Config 格式，
// 提取 API 连接相关的字段（BaseURL、APIKey、AuthToken）
// 用于与 gochat 库进行集成。
func (m *ModelConfig) Config() *gochat.Config {
	return &gochat.Config{
		BaseURL:   m.BaseURL,
		APIKey:    m.APIKey,
		AuthToken: m.AuthToken,
	}
}

// ResolveProvider 根据模型配置中的 Provider 字段，
// 从指定的 ProviderRegistry 中查找对应的提供商配置，
// 并用提供商的连接参数（BaseURL、APIKey、AuthToken）
// 填充模型配置中未设置的字段。如果 Provider 为空或
// registry 为 nil，则直接返回原始配置不做修改。
//
// 该方法实现了配置继承机制，允许在 Provider 层面统一管理
// 连接参数，避免在每个模型配置中重复填写。
func (m *ModelConfig) ResolveProvider(registry ProviderRegistry) *ModelConfig {
	if m.Provider == "" || registry == nil {
		return m
	}
	provider, err := registry.Get(m.Provider)
	if err != nil {
		return m
	}
	resolved := *m
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

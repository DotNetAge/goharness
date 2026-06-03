package config

// ProviderConfig 定义了 AI 服务提供商的配置结构。
// 它封装了与特定 API 提供商（如 OpenAI、Anthropic、Azure 等）
// 通信所需的连接参数和认证信息。
//
// ProviderConfig 作为 ModelConfig 的配置源，允许在提供商层面
// 统一管理 API 连接参数，多个模型可以共享同一个 Provider 的
// 连接配置，实现配置复用和集中管理。
type ProviderConfig struct {
	// Name 是提供商的唯一标识符，用于在 ProviderRegistry 中注册和引用。
	Name string `json:"name" yaml:"name"`

	// Title 提供了提供商的可读名称，用于 UI 展示和文档说明。
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// BaseURL 定义了提供商 API 的基础 URL 地址。
	// 例如：OpenAI 为 "https://api.openai.com/v1"，
	// Anthropic 为 "https://api.anthropic.com"。
	BaseURL string `json:"base_url" yaml:"base_url"`

	// APIKey 存储了访问提供商 API 的密钥凭证。
	APIKey string `json:"api_key" yaml:"api_key"`

	// AuthToken 存储了额外的认证令牌，用于 OAuth Bearer Token
	// 或其他自定义认证机制。当同时设置 APIKey 和 AuthToken 时，
	// 优先级取决于具体实现的认证策略。
	AuthToken string `json:"auth_token" yaml:"auth_token"`

	// IsLocal 标识该供应商是否为本地模型（如 Ollama、vLLM 等）。
	// 本地模型不需要 BaseURL 和 APIKey，直接通过本地端口访问。
	IsLocal bool `json:"is_local" yaml:"is_local"`
}

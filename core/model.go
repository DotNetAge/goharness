package core

import "github.com/DotNetAge/gochat/core"

// ModelConfig holds the configuration for an LLM backend.
// Provider-level fields (BaseURL, APIKey, AuthToken) can be inherited from
// a named ProviderConfig via the Provider field, or set directly as overrides.
type ModelConfig struct {
	// ID          string `json:"id" yaml:"id"`
	Name              string  `json:"name" yaml:"name"`                             // model name (internal identifier)
	Title             string  `json:"title,omitempty" yaml:"title,omitempty"`       // display title
	Description       string  `json:"description" yaml:"description"`               // model description
	Provider          string  `json:"provider" yaml:"provider"`                     // references a ProviderConfig by name
	BaseURL           string  `json:"base_url" yaml:"base_url"`                     // API base URL (overrides provider)
	APIKey            string  `json:"api_key" yaml:"api_key"`                       // API key (overrides provider)
	AuthToken         string  `json:"auth_token" yaml:"auth_token"`                 // auth token (overrides provider)
	MaxTokens         int64   `json:"max_tokens" yaml:"max_tokens"`                 // maximum output tokens per LLM call
	ContextLength     int64   `json:"context_length" yaml:"context_length"`         // total context window (input + output), 0 = unknown
	IsLocal           bool    `json:"is_local" yaml:"is_local"`                     // whether the model is local
	FuncCalling       bool    `json:"func_calling" yaml:"func_calling"`             // whether function calling is supported
	Structuring       bool    `json:"structuring" yaml:"structuring"`               // whether structured output is supported
	WebSearching      bool    `json:"web_searching" yaml:"web_searching"`           // whether web search is supported
	PrefixCon         bool    `json:"prefix_con" yaml:"prefix_con"`                 // whether prefix continuation is supported
	ContextCache      bool    `json:"context_cache" yaml:"context_cache"`           // whether context caching is supported
	TopP              float64 `json:"top_p" yaml:"top_p"`                           // top-p sampling parameter
	TopK              float64 `json:"top_k" yaml:"top_k"`                           // top-k sampling parameter
	Temperature       float64 `json:"temperature" yaml:"temperature"`               // sampling temperature
	RepetitionPenalty float64 `json:"repetition_penalty" yaml:"repetition_penalty"` // repetition penalty
	FrequencyPenalty  float64 `json:"frequency_penalty" yaml:"frequency_penalty"`   // frequency penalty (reduces exact token repetition)
	Enabled           bool    `json:"enabled" yaml:"enabled"`                       // whether the model is enabled (API key configured)
	MaxTurns          int     `json:"max_turns" yaml:"max_turns"`                   // maximum Think-Act loop iterations (0 = use reactor default)
}

func (m *ModelConfig) Config() *core.Config {
	return &core.Config{
		BaseURL:   m.BaseURL,
		APIKey:    m.APIKey,
		AuthToken: m.AuthToken,
	}
}

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

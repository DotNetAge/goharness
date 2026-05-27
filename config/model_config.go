package config

import gochat "github.com/DotNetAge/gochat/core"

type ModelConfig struct {
	Name              string  `json:"name" yaml:"name"`
	Title             string  `json:"title,omitempty" yaml:"title,omitempty"`
	Description       string  `json:"description" yaml:"description"`
	Provider          string  `json:"provider" yaml:"provider"`
	BaseURL           string  `json:"base_url" yaml:"base_url"`
	APIKey            string  `json:"api_key" yaml:"api_key"`
	AuthToken         string  `json:"auth_token" yaml:"auth_token"`
	MaxTokens         int64   `json:"max_tokens" yaml:"max_tokens"`
	ContextLength     int64   `json:"context_length" yaml:"context_length"`
	IsLocal           bool    `json:"is_local" yaml:"is_local"`
	FuncCalling       bool    `json:"func_calling" yaml:"func_calling"`
	Structuring       bool    `json:"structuring" yaml:"structuring"`
	WebSearching      bool    `json:"web_searching" yaml:"web_searching"`
	PrefixCon         bool    `json:"prefix_con" yaml:"prefix_con"`
	ContextCache      bool    `json:"context_cache" yaml:"context_cache"`
	TopP              float64 `json:"top_p" yaml:"top_p"`
	TopK              float64 `json:"top_k" yaml:"top_k"`
	Temperature       float64 `json:"temperature" yaml:"temperature"`
	RepetitionPenalty float64 `json:"repetition_penalty" yaml:"repetition_penalty"`
	FrequencyPenalty  float64 `json:"frequency_penalty" yaml:"frequency_penalty"`
	Enabled           bool    `json:"enabled" yaml:"enabled"`
	MaxTurns          int     `json:"max_turns" yaml:"max_turns"`
}

func (m *ModelConfig) Config() *gochat.Config {
	return &gochat.Config{
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

package core

import "github.com/DotNetAge/gochat/core"

// ModelConfig holds the configuration for an LLM backend.
type ModelConfig struct {
	// ID          string `json:"id" yaml:"id"`
	Name              string  `json:"name" yaml:"name"`                             // model name
	Description       string  `json:"description" yaml:"description"`               // model description
	BaseURL           string  `json:"base_url" yaml:"base_url"`                     // API base URL
	APIKey            string  `json:"api_key" yaml:"api_key"`                       // API key (or credential reference name)
	AuthToken         string  `json:"auth_token" yaml:"auth_token"`                 // auth token
	MaxTokens         int64   `json:"max_tokens" yaml:"max_tokens"`                 // maximum output tokens
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
	Enabled           bool    `json:"enabled" yaml:"enabled"`                       // whether the model is enabled (API key configured)
	MaxTurns          int     `json:"max_turns" yaml:"max_turns"`                   // maximum T-A-O loop iterations (0 = use reactor default)
}

func (m *ModelConfig) Config() *core.Config {
	return &core.Config{
		BaseURL:   m.BaseURL,
		APIKey:    m.APIKey,
		AuthToken: m.AuthToken,
	}
}

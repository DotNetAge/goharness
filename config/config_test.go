package config

import (
	"testing"
)

func TestModelConfig_DefaultValuesAndBoundaryCases(t *testing.T) {
	t.Run("zero values for model config", func(t *testing.T) {
		cfg := ModelConfig{}

		if cfg.Name != "" {
			t.Error("expected empty Name")
		}
		if cfg.ContextLength != 0 {
			t.Error("expected ContextLength to be 0")
		}
		if cfg.TopP != 0 {
			t.Error("expected TopP to be 0")
		}
		if cfg.Temperature != 0 {
			t.Error("expected Temperature to be 0")
		}
		if cfg.IsLocal {
			t.Error("expected IsLocal to be false")
		}
		if cfg.FuncCalling {
			t.Error("expected FuncCalling to be false")
		}
	})

	t.Run("boundary values for numeric fields", func(t *testing.T) {
		cfg := ModelConfig{
			ContextLength:     0,
			TopP:              0.0,
			TopK:              0.0,
			Temperature:       2.0,
			RepetitionPenalty: -1.0,
			FrequencyPenalty:  -2.0,
			MaxTurns:          -1,
		}

		if cfg.Temperature != 2.0 {
			t.Error("Temperature should be 2.0")
		}
		if cfg.RepetitionPenalty != -1.0 {
			t.Error("RepetitionPenalty should accept negative value")
		}
	})

	t.Run("boolean flags combinations", func(t *testing.T) {
		testCases := []struct {
			name     string
			config   ModelConfig
			expected []bool
		}{
			{
				name: "all enabled",
				config: ModelConfig{
					IsLocal: true, FuncCalling: true, Structuring: true,
					WebSearching: true, PrefixCon: true, ContextCache: true, Enabled: true,
				},
				expected: []bool{true, true, true, true, true, true, true},
			},
			{
				name: "all disabled",
				config: ModelConfig{
					IsLocal: false, FuncCalling: false, Structuring: false,
					WebSearching: false, PrefixCon: false, ContextCache: false, Enabled: false,
				},
				expected: []bool{false, false, false, false, false, false, false},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				flags := []bool{
					tc.config.IsLocal, tc.config.FuncCalling, tc.config.Structuring,
					tc.config.WebSearching, tc.config.PrefixCon, tc.config.ContextCache, tc.config.Enabled,
				}
				for i, got := range flags {
					if got != tc.expected[i] {
						t.Errorf("flag %d: expected %v, got %v", i, tc.expected[i], got)
					}
				}
			})
		}
	})

	t.Run("complete model config creation", func(t *testing.T) {
		cfg := ModelConfig{
			Name:          "gpt-4",
			Title:         "GPT-4",
			Description:   "Advanced language model",
			Provider:      "openai",
			BaseURL:       "https://api.openai.com/v1",
			APIKey:        "test-key",
			ContextLength: 128000,
			FuncCalling:   true,
			Structuring:   true,
			TopP:          0.9,
			Temperature:   0.7,
			Enabled:       true,
			MaxTurns:      10,
		}

		if cfg.Name != "gpt-4" {
			t.Error("unexpected name")
		}
		if cfg.Provider != "openai" {
			t.Error("unexpected provider")
		}
		if !cfg.FuncCalling || !cfg.Structuring {
			t.Error("flags not set correctly")
		}
	})
}

func TestProviderConfig_Creation(t *testing.T) {
	t.Run("complete provider config", func(t *testing.T) {
		provider := ProviderConfig{
			Name:      "openai",
			Title:     "OpenAI",
			BaseURL:   "https://api.openai.com/v1",
			APIKey:    "sk-test-key",
			AuthToken: "bearer-token",
		}

		if provider.Name != "openai" {
			t.Error("unexpected name")
		}
		if provider.BaseURL != "https://api.openai.com/v1" {
			t.Error("unexpected base URL")
		}
	})

	t.Run("minimal provider config", func(t *testing.T) {
		provider := ProviderConfig{
			Name:    "minimal",
			BaseURL: "http://localhost:11434",
		}

		if provider.Title != "" {
			t.Error("expected empty title")
		}
		if provider.APIKey != "" {
			t.Error("expected empty API key")
		}
	})
}

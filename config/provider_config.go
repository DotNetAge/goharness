package config

type ProviderConfig struct {
	Name      string `json:"name" yaml:"name"`
	Title     string `json:"title,omitempty" yaml:"title,omitempty"`
	BaseURL   string `json:"base_url" yaml:"base_url"`
	APIKey    string `json:"api_key" yaml:"api_key"`
	AuthToken string `json:"auth_token" yaml:"auth_token"`
}

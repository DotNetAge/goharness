package tools

import (
	"context"

	"github.com/DotNetAge/goreact/events"
)

type FuncTool interface {
	Info() *ToolInfo
	Execute(ctx context.Context, params map[string]any) (any, error)
}

type ToolInfo struct {
	Name               string               `json:"name" yaml:"name"`
	Description        string               `json:"description" yaml:"description"`
	Prompt             string               `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Tags               []string             `json:"tags" yaml:"tags"`
	SecurityLevel      events.SecurityLevel `json:"security_level" yaml:"security_level"`
	IsIdempotent       bool                 `json:"is_idempotent" yaml:"is_idempotent"`
	IsAsync            bool                 `json:"is_async,omitempty" yaml:"is_async,omitempty"`
	Parameters         []Parameter          `json:"parameters" yaml:"parameters"`
	ReturnType         string               `json:"return_type" yaml:"return_type"`
	Examples           []string             `json:"examples" yaml:"examples"`
	MaxResultSizeChars int                  `json:"max_result_size_chars,omitempty" yaml:"max_result_size_chars,omitempty"`
	IsReadOnly         bool                 `json:"is_read_only,omitempty" yaml:"is_read_only,omitempty"`
}

type Parameter struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Required    bool   `json:"required" yaml:"required"`
	Default     any    `json:"default" yaml:"default"`
	Description string `json:"description" yaml:"description"`
	Enum        []any  `json:"enum" yaml:"enum"`
}

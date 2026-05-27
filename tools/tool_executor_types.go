package tools

import (
	"context"
	"time"
)

type ToolExecutionResult struct {
	Result   string
	Metadata any
	Duration time.Duration
	Error    error
	ToolName string
}

type ToolExecutor interface {
	Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error)
	ResetCycle()
}

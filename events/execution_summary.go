package events

import (
	"time"

	"github.com/DotNetAge/goharness/session"
)

type ExecutionSummaryData struct {
	TotalIterations   int                `json:"total_iterations"`
	ToolCalls         int                `json:"tool_calls"`
	ToolsUsed         []string           `json:"tools_used,omitempty"`
	TotalDuration     time.Duration      `json:"total_duration_ns"`
	TokensUsed        session.TokenUsage `json:"tokens_used"`
	TerminationReason string             `json:"termination_reason,omitempty"`
}

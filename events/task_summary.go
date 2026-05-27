package events

import "github.com/DotNetAge/goreact/session"

type TaskSummaryData struct {
	Summary    string             `json:"summary"`
	TokenUsage session.TokenUsage `json:"token_usage"`
}

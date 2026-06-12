package events

import "github.com/DotNetAge/goharness/session"

type TaskSummaryData struct {
	Summary    string             `json:"summary"`
	TokenUsage session.TokenUsage `json:"token_usage"`
}

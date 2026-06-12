package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/DotNetAge/goharness/events"
)

// AgentTalkFunc sends a message to another agent in a specific session and returns the reply.
// The target agent is created on-demand via Clone if not already running.
// The sessionID is caller-specified — use a new ID for first contact, or reuse an existing
// one to continue a previous conversation thread.
type AgentTalkFunc func(ctx context.Context, to, sessionID, message string) (string, error)

// AgentTalkTool lets the LLM send messages to other agents and receive replies.
// Unlike SubAgent (one-shot task delegation), AgentTalk maintains a conversation
// thread via sessionID — the target agent sees the full history when sessionID is reused.
type AgentTalkTool struct {
	talk AgentTalkFunc
}

func NewAgentTalkTool(talk AgentTalkFunc) *AgentTalkTool {
	return &AgentTalkTool{talk: talk}
}

func (t *AgentTalkTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "AgentTalk",
		Description: "Send a message to another agent and get a reply.",
		Prompt: `Send a message to another agent and get an immediate reply. This tool is SYNC — the reply arrives in the same round.

Unlike SubAgent (one-shot, async), AgentTalk maintains a conversation thread via session_id.

Key: reuse the same session_id for follow-ups so the agent remembers context. Pick a meaningful ID for first contact (e.g., "project-review").`,
		Tags: []string{"orchestration", "talk", "agent-talk", "coordination"},
		Parameters: []Parameter{
			{Name: "agent_name", Type: "string", Description: "Target agent name (e.g. @writer, code-reviewer).", Required: true},
			{Name: "session_id", Type: "string", Description: "Conversation thread ID. New = start fresh. Reused = continue where you left off.", Required: true},
			{Name: "message", Type: "string", Description: "Message content. Include enough context for the agent to understand.", Required: true},
		},
	}
}

func (t *AgentTalkTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	to, _ := params["agent_name"].(string)
	if to == "" {
		return nil, fmt.Errorf("agent_name is required")
	}

	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	message, _ := params["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}

	if t.talk == nil {
		return nil, fmt.Errorf("AgentTalk tool: AgentTalkFunc not configured")
	}

	logger := getLogger(ctx)
	logger.Info("agent talk",
		"to", to,
		"session_id", sessionID,
		"message_preview", truncateForLog(message, 100),
	)

	tc := GetToolContext(ctx)
	if tc != nil && tc.EmitEvent != nil {
		tc.EmitEvent(events.ReactEvent{
			Type: events.AgentTalkStart,
			Data: events.AgentTalkInfo{To: to, SessionID: sessionID, Message: message},
		})
	}

	started := time.Now()
	reply, err := t.talk(ctx, to, sessionID, message)
	if err != nil {
		logger.Error("agent talk failed", err,
			"to", to,
			"session_id", sessionID,
			"elapsed_ms", time.Since(started).Milliseconds(),
		)
		if tc != nil && tc.EmitEvent != nil {
			tc.EmitEvent(events.ReactEvent{
				Type: events.AgentTalkEnd,
				Data: events.AgentTalkResult{To: to, SessionID: sessionID, Error: err.Error()},
			})
		}
		return nil, fmt.Errorf("agent talk to %q: %w", to, err)
	}

	if tc != nil && tc.EmitEvent != nil {
		tc.EmitEvent(events.ReactEvent{
			Type: events.AgentTalkEnd,
			Data: events.AgentTalkResult{To: to, SessionID: sessionID, Reply: reply},
		})
	}

	return map[string]any{
		"reply":      reply,
		"agent_name": to,
		"session_id": sessionID,
	}, nil
}

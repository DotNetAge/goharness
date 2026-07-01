package events

import "time"

type ReactEvent struct {
	SessionID string         `json:"session_id"`
	AgentName string         `json:"agent_name,omitempty"`
	TaskID    string         `json:"task_id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Type      ReactEventType `json:"type"`
	Data      any            `json:"data,omitempty"`
	Timestamp int64          `json:"timestamp"`
}

func NewReactEvent(sessionID, taskID, parentID string, eventType ReactEventType, data any) ReactEvent {
	return ReactEvent{
		SessionID: sessionID,
		TaskID:    taskID,
		ParentID:  parentID,
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	}
}

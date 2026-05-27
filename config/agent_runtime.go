package config

import "time"

type AgentState string

const (
	AgentStateIdle         AgentState = "idle"
	AgentStateBusy         AgentState = "busy"
	AgentStateCoordinating AgentState = "coordinating"
	AgentStateDormant      AgentState = "dormant"
	AgentStateError        AgentState = "error"
)

func (s AgentState) IsTerminal() bool {
	switch s {
	case AgentStateError:
		return true
	default:
		return false
	}
}

func (s AgentState) CanAcceptTask() bool {
	switch s {
	case AgentStateIdle, AgentStateDormant:
		return true
	default:
		return false
	}
}

type AgentRuntimeMeta struct {
	Config     *AgentConfig
	State      AgentState
	Score      float64
	TaskCount  int64
	LastActive time.Time
}

func NewAgentRuntimeMeta(config *AgentConfig) *AgentRuntimeMeta {
	if config == nil {
		panic("goreact: NewAgentRuntimeMeta called with nil AgentConfig")
	}
	return &AgentRuntimeMeta{
		Config:     config,
		State:      AgentStateIdle,
		Score:      0,
		TaskCount:  0,
		LastActive: time.Now(),
	}
}

func (m *AgentRuntimeMeta) ID() string          { return m.Config.Name }
func (m *AgentRuntimeMeta) Name() string        { return m.Config.Name }
func (m *AgentRuntimeMeta) Description() string { return m.Config.Description }
func (m *AgentRuntimeMeta) IsActive() bool      { return m.State != AgentStateError }
func (m *AgentRuntimeMeta) IsAvailable() bool {
	return m.State == AgentStateIdle || m.State == AgentStateDormant
}

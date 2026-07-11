package config

import "time"

// AgentState 定义了 Agent 在运行时的状态类型。
// 状态用于跟踪 Agent 的当前工作状态，影响任务分配和调度决策。
type AgentState string

const (
	// AgentStateIdle 表示 Agent 处于空闲状态，可以接受新任务。
	AgentStateIdle AgentState = "idle"

	// AgentStateBusy 表示 Agent 正在执行任务，暂时无法接受新任务。
	AgentStateBusy AgentState = "busy"

	// AgentStateCoordinating 表示 Agent 正在协调其他 Agent 的工作，
	// 通常在多 Agent 协作编排场景中出现。
	AgentStateCoordinating AgentState = "coordinating"

	// AgentStateDormant 表示 Agent 处于休眠状态，可以接受任务但可能需要唤醒。
	AgentStateDormant AgentState = "dormant"

	// AgentStateError 表示 Agent 处于错误状态，无法正常工作，
	// 需要人工干预或自动恢复机制来处理。
	AgentStateError AgentState = "error"
)

// IsTerminal 判断当前状态是否为终止状态。
// 终止状态的 Agent 无法通过正常的任务流程恢复，
// 目前只有 Error 被视为终止状态。
//
// 返回 true 表示该状态是终态，Agent 需要特殊处理才能重新投入使用。
func (s AgentState) IsTerminal() bool {
	switch s {
	case AgentStateError:
		return true
	default:
		return false
	}
}

// CanAcceptTask 判断当前状态下 Agent 是否可以接受新任务。
// 只有 Idle 和 Dormant 状态的 Agent 可以接受任务分配，
// Busy、Coordinating 和 Error 状态的 Agent 会拒绝新任务。
//
// 该方法用于任务调度器判断是否可以将任务分配给该 Agent。
func (s AgentState) CanAcceptTask() bool {
	switch s {
	case AgentStateIdle, AgentStateDormant:
		return true
	default:
		return false
	}
}

// AgentRuntimeMeta 存储了 Agent 的运行时元数据信息。
// 它将静态配置（AgentConfig）与动态运行时信息（状态、评分等）结合在一起，
// 为 RuntimeDirectory 提供完整的管理视图。
//
// AgentRuntimeMeta 是 RuntimeDirectory 管理的核心数据结构，
// 每个注册到 RuntimeDirectory 的 Agent 都有一个对应的实例。
type AgentRuntimeMeta struct {
	// Config 关联了 Agent 的静态配置，包含名称、角色、模型等信息。
	Config *AgentConfig

	// State 记录了 Agent 当前的运行状态（Idle/Busy/Coordinating/Dormant/Error）。
	State AgentState

	// Score 存储了 Agent 的评分值，用于排序和选择最优 Agent。
	// 评分可以基于历史表现、响应时间、成功率等因素计算。
	Score float64

	// TaskCount 记录了 Agent 已处理的任务总数，用于负载均衡和工作量评估。
	TaskCount int64

	// LastActive 记录了 Agent 最后一次活动的时间戳，
	// 用于判断 Agent 是否超时或需要健康检查。
	LastActive time.Time
}

// NewAgentRuntimeMeta 创建并返回一个新的 AgentRuntimeMeta 实例。
//
// 参数 config 必须不为 nil，否则会触发 panic。
// 创建的实例使用以下默认值：
//   - State: AgentStateIdle（空闲状态）
//   - Score: 0.0（初始评分）
//   - TaskCount: 0（无历史任务）
//   - LastActive: time.Now()（创建时间）
func NewAgentRuntimeMeta(config *AgentConfig) *AgentRuntimeMeta {
	if config == nil {
		panic("goharness: NewAgentRuntimeMeta 被调用时传入了为nil值的AgentConfig")
	}
	return &AgentRuntimeMeta{
		Config:     config,
		State:      AgentStateIdle,
		Score:      0,
		TaskCount:  0,
		LastActive: time.Now(),
	}
}

// ID 返回 Agent 的唯一标识符，即配置中的 Name 字段。
// 用于在 RuntimeDirectory 中作为键进行索引和查找。
func (m *AgentRuntimeMeta) ID() string { return m.Config.Name }

// Name 返回 Agent 的显示名称，即配置中的 Name 字段。
func (m *AgentRuntimeMeta) Name() string { return m.Config.Name }

// Description 返回 Agent 的描述信息，用于搜索匹配和展示。
func (m *AgentRuntimeMeta) Description() string { return m.Config.Description }

// IsActive 判断 Agent 是否处于活跃状态（非 Error 状态）。
// 活跃的 Agent 包括 Idle、Busy、Coordinating 和 Dormant 状态。
//
// 只有 Error 状态被视为非活跃，表示 Agent 出现异常无法正常工作。
func (m *AgentRuntimeMeta) IsActive() bool { return m.State != AgentStateError }

// IsAvailable 判断 Agent 是否可用于接收新任务。
// 可用的 Agent 必须处于 Idle 或 Dormant 状态，
// 这些状态的 Agent 可以立即接受任务分配。
//
// 与 IsActive 不同，IsAvailable 要求更严格：
// Busy 和 Coordinating 状态虽然活跃但不可用。
func (m *AgentRuntimeMeta) IsAvailable() bool {
	return m.State == AgentStateIdle || m.State == AgentStateDormant
}

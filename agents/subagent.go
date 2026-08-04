package agents

import (
	"context"
	"fmt"
	"sync"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// parentEmitKeyType 用于在 context 中传递父级 EventBus 发射器，
// 使子智能体能够将事件转发到父级事件总线。
type parentEmitKeyType struct{}

// subAgentManager 管理子智能体的会话缓存与派生执行，是 Runtime 的一个内聚子系统，
// 从 Runtime 抽离以减轻后者的职责密度。
//
// 职责：
//   - 按 "agentName:projectDir" 缓存子智能体会话，保证同一子智能体在同一项目中复用同一会话，
//     维持对话连续性。
//   - 通过 Runtime.Ask 运行子智能体的独立思考循环（与父级上下文隔离）。
//
// 依赖经 rt 反向引用（子系统持有父编排器的引用是 Go 常见模式，相比传递多个回调更清晰）：
//   - rt.Ask：运行子智能体思考循环
//   - rt.prompt.agentReg：校验子智能体配置是否存在
//   - rt.SessionConfigs()：为新建子会话注入 Compactor / Sandbox 等通用能力
//   - rt.logger：日志
type subAgentManager struct {
	rt    *Runtime
	mu    sync.RWMutex
	cache map[string]*session.Session
}

// newSubAgentManager 创建子智能体管理器。rt 为所属 Runtime，用于获取编排能力。
func newSubAgentManager(rt *Runtime) *subAgentManager {
	return &subAgentManager{
		rt:    rt,
		cache: make(map[string]*session.Session),
	}
}

// getOrCreate 返回指定 (agentName, projectDir) 对应的缓存会话；若不存在则创建。
// 保证同一项目下的同一子智能体始终复用同一会话，从而维持对话连续性。
// 创建失败时返回 nil（错误已记录日志）。
func (m *subAgentManager) getOrCreate(agentName, projectDir, sponsor string, store session.SessionStore) *session.Session {
	key := agentName + ":" + projectDir

	m.mu.RLock()
	if s, ok := m.cache[key]; ok {
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// 双重检查，避免加锁期间其他 goroutine 已创建。
	if s, ok := m.cache[key]; ok {
		return s
	}

	s, err := session.New(agentName, sponsor, projectDir, store, m.rt.logger, m.rt.SessionConfigs()...)
	if err != nil {
		m.rt.logger.Error("创建子智能体会话失败", err, "agent", agentName, "project", projectDir)
		return nil
	}
	m.cache[key] = s
	return s
}

// spawn 创建并运行子智能体（实现 tools.SpawnFunc）。
// 子智能体通过 Runtime.Ask 运行独立思考循环，结果随后通过 CollectResultsTool 收集。
//
// 设计决策：
//   - 复用 getOrCreate 缓存的会话：同一 (agentName, projectDir) 始终复用同一会话，
//     维持对话连续性（而非每次创建新会话）。
//   - 通过 Runtime.Ask 运行，与主智能体使用相同的思考循环。
//   - 独立会话意味着与父级上下文完全隔离。
func (m *subAgentManager) spawn(ctx context.Context, agentName, task string) (answer string, sessionID string, err error) {
	if m.rt.prompt.agentReg != nil {
		if cfg := m.rt.prompt.agentReg.Get(agentName); cfg == nil {
			return "", "", fmt.Errorf("未找到智能体配置: %q", agentName)
		}
	}

	tc := tools.GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return "", "", fmt.Errorf("上下文未包含会话")
	}
	sess := m.getOrCreate(agentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store())
	if sess == nil {
		// getOrCreate 已记录失败日志，此处返回错误让调用方感知。
		return "", "", fmt.Errorf("获取子智能体会话失败: %q", agentName)
	}
	m.rt.logger.Info("sub-agent spawn started",
		"agent_name", agentName,
		"session_id", sess.ID(),
	)

	builder := m.rt.Ask(agentName, task, sess)
	// 将子智能体的事件转发到父级 EventBus，
	// 以便订阅父级的客户端能够看到所有智能体事件。
	if pe, ok := ctx.Value(parentEmitKeyType{}).(func(events.ReactEvent)); ok {
		builder.parentEmit = pe
	}
	result, err := builder.Run()
	sessionID = sess.ID()
	if err != nil {
		return "", sessionID, fmt.Errorf("sub-agent %q: %w", agentName, err)
	}

	return result.Answer, sessionID, nil
}

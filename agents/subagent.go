package agents

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// parentEmitKeyType 用于在 context 中传递父级 EventBus 发射器，
// 使子智能体能够将事件转发到父级事件总线。
type parentEmitKeyType struct{}

// getOrCreateSubAgentSession 返回指定 (agentName, projectDir) 对应的缓存会话；
// 若不存在则创建新会话。该函数保证同一项目下的同一子智能体始终复用同一会话，
// 从而维持对话连续性。
func (rt *Runtime) getOrCreateSubAgentSession(agentName, projectDir, sponsor string, store session.SessionStore) *session.Session {
	key := agentName + ":" + projectDir

	rt.subAgentSessionMu.RLock()
	if s, ok := rt.subAgentSessionCache[key]; ok {
		rt.subAgentSessionMu.RUnlock()
		return s
	}
	rt.subAgentSessionMu.RUnlock()

	rt.subAgentSessionMu.Lock()
	defer rt.subAgentSessionMu.Unlock()

	// 双重检查，避免加锁期间其他 goroutine 已创建。
	if s, ok := rt.subAgentSessionCache[key]; ok {
		return s
	}

	s, err := session.New(agentName, sponsor, projectDir, store, rt.logger, rt.sessionOpts()...)
	if err != nil {
		rt.logger.Error("创建子智能体会话失败", err, "agent", agentName, "project", projectDir)
		return nil
	}
	rt.subAgentSessionCache[key] = s
	return s
}

// sessionOpts 返回 Runtime 应注入到所有子会话的 SessionConfig 列表。
// 当前仅注入沙箱（若已配置）；未来可扩展其他自动注入项。
// 返回 nil 时 session.New 使用默认配置，不影响现有行为。
func (rt *Runtime) sessionOpts() []session.SessionConfig {
	if rt.sandbox == nil {
		return nil
	}
	return []session.SessionConfig{session.WithSandbox(rt.sandbox)}
}

// spawnSubAgent 是创建并运行子智能体的 SpawnFunc 实现。
// 子智能体在后台通过独立的思考循环运行，结果随后通过 CollectResultsTool 收集。
//
// 设计决策：
//   - 每次 spawn 创建唯一会话（不复用会话，一次性任务）。
//   - 通过 Runtime.Ask() 运行，与主智能体使用相同的思考循环。
//   - 独立会话意味着与父级上下文完全隔离。
func (rt *Runtime) spawnSubAgent(ctx context.Context, agentName, task string) (answer string, sessionID string, err error) {
	if rt.agentReg != nil {
		if cfg := rt.agentReg.Get(agentName); cfg == nil {
			return "", "", fmt.Errorf("未找到智能体配置: %q", agentName)
		}
	}

	tc := tools.GetToolContext(ctx)
	sess := rt.getOrCreateSubAgentSession(agentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store())
	rt.logger.Info("sub-agent spawn started",
		"agent_name", agentName,
		"session_id", sess.ID(),
	)

	builder := rt.Ask(agentName, task, sess)
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

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

	s, err := session.New(agentName, sponsor, projectDir, store, rt.logger, rt.SessionConfigs()...)
	if err != nil {
		rt.logger.Error("创建子智能体会话失败", err, "agent", agentName, "project", projectDir)
		return nil
	}
	rt.subAgentSessionCache[key] = s
	return s
}

// SessionConfigs 返回 Runtime 应注入到所有会话（主会话与子会话）的通用 SessionConfig 列表。
//
// 这里注入的是 Runtime 提供的通用能力，所有会话共享：
//   - Compactor：上下文压缩引擎。其依赖（llmClient/model/请求构造路径）全部来自
//     Runtime，是 goharness 内置能力，无需外部应用注入——外部只需在创建会话时
//     合并本方法返回的配置即可启用压缩。
//   - Sandbox：工具安全沙箱（若已配置）。
//
// 会话特有的配置（如 MemoryStore、ModelContextResolver，依赖会话级数据）由调用方
// 在创建会话时额外追加，不应放入此处。
func (rt *Runtime) SessionConfigs() []session.SessionConfig {
	opts := []session.SessionConfig{
		session.WithCompactor(NewCompactor(rt)),
	}
	if rt.sandbox != nil {
		opts = append(opts, session.WithSandbox(rt.sandbox))
	}
	return opts
}

// spawnSubAgent 是创建并运行子智能体的 SpawnFunc 实现。
// 子智能体在后台通过独立的思考循环运行，结果随后通过 CollectResultsTool 收集。
//
// 设计决策：
//   - 每次 spawn 创建唯一会话（不复用会话，一次性任务）。
//   - 通过 Runtime.Ask() 运行，与主智能体使用相同的思考循环。
//   - 独立会话意味着与父级上下文完全隔离。
func (rt *Runtime) spawnSubAgent(ctx context.Context, agentName, task string) (answer string, sessionID string, err error) {
	if rt.prompt.agentReg != nil {
		if cfg := rt.prompt.agentReg.Get(agentName); cfg == nil {
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

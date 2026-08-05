package agents

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// parentEmitKeyType 用于在 context 中传递父级 EventBus 发射器，
// 使子智能体能够将事件转发到父级事件总线。
type parentEmitKeyType struct{}

// perSessionState 是单个子代理会话的执行状态：
// 包含会话实例与其专属互斥锁。锁粒度覆盖整个 exec 循环，
// 保证同一会话的并发 Ask 串行执行（per-session 互斥），杜绝消息交错。
type perSessionState struct {
	sess *session.Session
	lock sync.Mutex

	// pendingCh 是子智能体授权冒泡的等待通道。
	// 子会话 exec 挂起等待授权时由 registerPermissionWait 登记，
	// 主会话经 dispatchPermission 向该通道发送授权决策；
	// exec 结束后由 clearPermissionWait 清除并关闭。nil 表示未挂起等待授权。
	pendingCh chan permissionSignal

	// pendingAt 记录子会话开始挂起等待授权的纳秒时间戳。
	// 多个子会话同时挂起时，主会话按此时间戳先到先服务路由魔法词。
	pendingAt int64

	// lastUsedAt 记录最近一次被 spawn 认领使用的时间。
	// 空闲会话复用（findIdleSession）据此选择"最近使用"的会话；
	// 零值表示从未被 spawn 认领（刚创建/恢复，即将被其 spawn 锁定使用），
	// 不参与复用——避免并行分身场景中，一个会话刚创建尚未被其 spawn
	// 锁定时被其他并发调用误复用。
	lastUsedAt time.Time
}

// subAgentManager 管理子智能体的会话登记与派生执行，是 Runtime 的一个内聚子系统，
// 从 Runtime 抽离以减轻后者的职责密度。
//
// 职责：
//   - 以 SessionID 为唯一键登记子智能体会话（1 ProjectDir → N Session 分身模型）：
//     不传 sessionID 时优先复用同 Agent + ProjectDir + Sponsor 的空闲会话延续上下文，
//     无空闲候选才新建独立会话（分身/并行委派）；传 sessionID 时复用已登记实例或从 store 恢复。
//   - 通过 Runtime.Ask 运行子智能体的独立思考循环（与父级上下文隔离）。
//   - 以 per-session 互斥锁串行化同一会话的并发 Ask。
//
// 依赖经 rt 反向引用（子系统持有父编排器的引用是 Go 常见模式，相比传递多个回调更清晰）：
//   - rt.Ask：运行子智能体思考循环
//   - rt.prompt.agentReg：校验子智能体配置是否存在
//   - rt.SessionConfigs()：为新建子会话注入 Compactor / Sandbox 等通用能力
//   - rt.logger：日志
type subAgentManager struct {
	rt     *Runtime
	mu     sync.RWMutex
	states map[string]*perSessionState
}

// newSubAgentManager 创建子智能体管理器。rt 为所属 Runtime，用于获取编排能力。
func newSubAgentManager(rt *Runtime) *subAgentManager {
	return &subAgentManager{
		rt:     rt,
		states: make(map[string]*perSessionState),
	}
}

// getOrCreate 返回指定 sessionID 对应的子代理会话状态；不存在则创建/恢复。
//
// 定位规则（SessionID 唯一定位）：
//   - sessionID 非空：命中已登记状态则复用；否则调用 session.Load 从 store 恢复旧会话，
//     实现「延续对话」。
//   - sessionID 为空：优先复用空闲会话或恢复最近使用的会话延续上下文，无候选才新建分身——
//     同一 Agent 在同一 ProjectDir 且同一 Sponsor 下的讨论话题相关度高、上下文连续，
//     持续新开会话会丢失正在讨论的上下文，导致重复读取文件浪费算力。
//
// 失败时返回错误（不再静默返回 nil）。
func (m *subAgentManager) getOrCreate(ctx context.Context, agentName, projectDir, sponsor string, store session.SessionStore, sessionID string) (*perSessionState, error) {
	// 显式复用时先读快照：已登记的会话直接命中，避免重复加载。
	if sessionID != "" {
		m.mu.RLock()
		st, ok := m.states[sessionID]
		m.mu.RUnlock()
		if ok {
			return st, nil
		}
	}

	// sessionID 为空：先尝试复用空闲会话（内存）或恢复最近会话（持久化存储），
	// 无候选才新建。注意此处不能持有 m.mu——
	// findIdleSession / recoverLatestSession 内部自行加锁，避免写锁内嵌套读锁死锁。
	if sessionID == "" {
		if st := m.findIdleSession(agentName, projectDir, sponsor); st != nil {
			return st, nil
		}
		if st := m.recoverLatestSession(ctx, agentName, projectDir, sponsor, store); st != nil {
			return st, nil
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 双重检查，避免加锁期间其他 goroutine 已登记。
	if sessionID != "" {
		if st, ok := m.states[sessionID]; ok {
			return st, nil
		}
	}

	var (
		s   *session.Session
		err error
	)
	if sessionID != "" {
		// 显式复用：从持久化存储按 ID 恢复会话。
		// 身份由 SessionID 决定，agentName 从元数据恢复，避免调用方传错导致身份漂移。
		s, err = session.Load(ctx, sessionID, "", store, m.rt.logger, m.rt.SessionConfigs()...)
		if err != nil {
			return nil, fmt.Errorf("加载子智能体会话失败: %w", err)
		}
	} else {
		// 新建分身：同一 Agent 同一目录且无空闲可复用会话时产生新会话。
		// 必须通过 store.Create 持久化会话元数据（含 project_dir 与 sponsor）——
		// 若直接 session.New，meta.json 由首次 Append 兜底创建时 project_dir 为空，
		// 导致 daemon 重启后 session.Load 无法恢复工作目录（表现为 ProjectDir 丢失）。
		// 创建后再按新生成的会话 ID 加载，行为与「显式复用」路径保持一致。
		info, createErr := store.Create(ctx, agentName,
			session.WithProjectDirOption(projectDir),
			session.WithSponsorOption(sponsor),
		)
		if createErr != nil {
			return nil, fmt.Errorf("创建子智能体会话失败: %w", createErr)
		}
		s, err = session.Load(ctx, info.SessionID, agentName, store, m.rt.logger, m.rt.SessionConfigs()...)
		if err != nil {
			return nil, fmt.Errorf("加载子智能体会话失败: %w", err)
		}
	}
	st := &perSessionState{sess: s}
	m.states[s.ID()] = st
	return st, nil
}

// findIdleSession 在内存已登记会话中查找可复用的空闲会话。
// 匹配条件：AgentName / ProjectDir / Sponsor 三者一致——同一发起方（Sponsor）在
// 同一工作目录（ProjectDir）下安排同一 Agent，讨论的话题相关度高，上下文连续可延续；
// 且会话已被 spawn 认领使用过（lastUsedAt 非零）、当前空闲（per-session 锁未被持有）。
// 若该 Agent 正有活跃 spawn 并行工作，则不复用（返回 nil，由调用方新建分身保持并行）。
// 多个候选时选择最近使用（lastUsedAt 最新）的会话。
func (m *subAgentManager) findIdleSession(agentName, projectDir, sponsor string) *perSessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best *perSessionState
	for _, st := range m.states {
		if st.sess.AgentName() != agentName ||
			st.sess.ProjectDir() != projectDir ||
			st.sess.Sponsor() != sponsor {
			continue
		}
		if st.lastUsedAt.IsZero() {
			// 从未被 spawn 认领（刚创建/恢复，即将被其 spawn 锁定使用）：
			// 不参与复用，避免并行分身场景下误复用到同一新会话。
			continue
		}
		if !st.lock.TryLock() {
			// 当前有活跃 spawn —— 并行分身场景，跳过复用。
			continue
		}
		st.lock.Unlock()
		if best == nil || st.lastUsedAt.After(best.lastUsedAt) {
			best = st
		}
	}
	return best
}

// recoverLatestSession 从持久化存储恢复同 Agent + ProjectDir + Sponsor 的最近使用会话，
// 实现跨进程重启后的上下文延续（读取最新会话恢复讨论上下文）。
// 返回 nil 表示存储不可用、无可匹配会话，或候选会话正被活跃 spawn 使用（并行分身）。
func (m *subAgentManager) recoverLatestSession(ctx context.Context, agentName, projectDir, sponsor string, store session.SessionStore) *perSessionState {
	if store == nil {
		return nil
	}
	infos, err := store.ListSessions(ctx)
	if err != nil {
		return nil
	}
	var latest session.SessionInfo
	found := false
	for _, info := range infos {
		if info.SessionID == "" ||
			info.AgentName != agentName ||
			info.ProjectDir != projectDir ||
			info.Sponsor != sponsor {
			continue
		}
		if !found || info.LastActivityAt.After(latest.LastActivityAt) {
			latest = info
			found = true
		}
	}
	if !found {
		return nil
	}

	m.mu.RLock()
	_, inMem := m.states[latest.SessionID]
	m.mu.RUnlock()
	if inMem {
		// 已在内存中：是否可复用（空闲且使用过）由 findIdleSession 判定，
		// 此处一律返回 nil——否则并行分身场景下会误复用到刚创建、尚未被其
		// spawn 认领的新会话，导致并行任务被错误串行化。
		return nil
	}

	// 从存储加载历史会话并登记。
	s, err := session.Load(ctx, latest.SessionID, agentName, store, m.rt.logger, m.rt.SessionConfigs()...)
	if err != nil {
		return nil
	}
	st := &perSessionState{sess: s}
	m.mu.Lock()
	if exist, ok := m.states[s.ID()]; ok {
		m.mu.Unlock()
		return exist
	}
	m.states[s.ID()] = st
	m.mu.Unlock()
	return st
}

// touchSession 记录会话最近一次被 spawn 认领使用的时间。
// findIdleSession 据此选择"最近使用"的空闲会话进行复用。
func (m *subAgentManager) touchSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[sessionID]; st != nil {
		st.lastUsedAt = time.Now()
	}
}

// spawn 创建并运行子智能体（实现 tools.SpawnFunc）。
// 子智能体通过 Runtime.Ask 运行独立思考循环，结果随后通过 CollectResultsTool 收集。
//
// 设计决策：
//   - 会话定位遵循「空闲复用 + 并行分身 + 显式复用」：sessionID 为空 → 优先复用
//     同 Agent + ProjectDir + Sponsor 的空闲会话延续上下文（避免重复读取文件浪费算力），
//     无空闲候选则新建独立会话（分身，支持并行委派）；sessionID 非空 → 复用旧会话。
//   - per-session 互斥锁：同一会话的并发 Ask 串行执行，从根上杜绝消息交错
//     （assistant tool_calls → tool 配对被并发写破坏）；不同会话之间完全并行。
//   - 通过 Runtime.Ask 运行，与主智能体使用相同的思考循环。
//   - 独立会话意味着与父级上下文完全隔离。
func (m *subAgentManager) spawn(ctx context.Context, agentName, task, sessionID string) (answer string, sid string, err error) {
	if m.rt.prompt.agentReg != nil {
		if cfg := m.rt.prompt.agentReg.Get(agentName); cfg == nil {
			return "", "", fmt.Errorf("未找到智能体配置: %q", agentName)
		}
	}

	tc := tools.GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return "", "", fmt.Errorf("上下文未包含会话")
	}
	st, err := m.getOrCreate(ctx, agentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store(), sessionID)
	if err != nil {
		return "", "", fmt.Errorf("获取子智能体会话失败: %q: %w", agentName, err)
	}
	sess := st.sess

	// per-session 互斥：同一会话的并发 Ask 串行执行。
	// 锁粒度 = 完整 exec 循环，持有期间阻塞其他对该会话的 spawn。
	st.lock.Lock()
	defer st.lock.Unlock()

	// 记录本次使用时间：后续同一 Agent + ProjectDir + Sponsor 的 spawn
	// 可据此判断空闲会话并复用（延续讨论上下文）。
	m.touchSession(sess.ID())

	m.rt.logger.Info("sub-agent spawn started",
		"agent_name", agentName,
		"session_id", sess.ID(),
	)

	// 任务边界标记：复用已有会话（延续上下文）时，历史任务的最终答案/终止标记
	// 仍保留在会话消息中。在新任务的问题消息（exec 追加）之前插入一条 user 角色
	// 任务开始标记，使 CollectResults 的 findFinalAnswer 能划定任务边界——
	// 避免新任务尚未完成时提前命中旧任务的结果（直接返回旧答案/旧终止标记）。
	// 全新会话无历史消息，无需标记；标记追加失败仅告警，不阻断任务（Append 不依赖 ctx 取消）。
	if len(sess.All()) > 0 {
		if appendErr := sess.Append(ctx, session.Message{
			Role:      "user",
			Content:   tools.SubAgentTaskStartPrefix + " 新的子任务开始，请结合此前的讨论上下文完成新任务。",
			Timestamp: time.Now().Unix(),
		}); appendErr != nil {
			m.rt.logger.Warn("追加子智能体任务开始标记失败",
				"session", sess.ID(), "error", appendErr)
		}
	}

	builder := m.rt.Ask(agentName, task, sess)
	// 将子智能体的事件转发到父级 EventBus，
	// 以便订阅父级的客户端能够看到所有智能体事件。
	if pe, ok := ctx.Value(parentEmitKeyType{}).(func(events.ReactEvent)); ok {
		builder.parentEmit = pe
	}
	// 子智能体授权冒泡旁路：从 ctx 读取授权请求直达前端的发送器并注入 builder。
	// 子会话触发授权时优先经它发送授权请求，不依赖父 exec EventBus 的存活
	// （父 exec 结束/被取消后订阅销毁会静默丢事件）；ctx 中的值由宿主
	// （mindx daemon）在派发子任务时经 WithPermissionSink 注入。
	if sink, ok := ctx.Value(permissionSinkKeyType{}).(PermissionSink); ok {
		builder.permissionSink = sink
	}
	// 子智能体授权冒泡：创建权限信号通道并注入 builder。
	// 子会话 exec 遇到需要授权的工具时通过该通道挂起等待主会话（用户）的授权决策，
	// 授权后继续执行；超时则以 permission_timeout 终止。
	permissionCh := make(chan permissionSignal, 1)
	builder.permissionCh = permissionCh
	// 通道生命周期随本 spawn 结束而结束：解除挂起登记并关闭通道。
	// waitForPermissionDecision 每次挂起/恢复也会解除登记，但不关闭通道——
	// 同一 spawn 内可能多次触发授权（授权后继续循环又遇到需授权的工具），
	// 通道必须保持可用，直到整个执行循环结束。
	defer func() {
		m.clearPermissionWait(sess.ID(), permissionCh)
		close(permissionCh)
	}()

	result, err := builder.Run()

	sid = sess.ID()

	// 子会话无最终答案的非正常终止（授权超时、上下文取消、执行错误等）：
	// 追加终止标记，供 CollectResults 快速判定失败，避免轮询死等到默认 30 分钟超时。
	// 错误场景同样写入标记（如 llm_error / cancelled），否则 CollectResults 无法区分
	// "子会话还在运行" 与 "已静默终止"，会死等轮询。
	if result.Answer == "" {
		marker := session.Message{
			Role:      "assistant",
			Content:   tools.SubAgentTerminatedPrefix + " " + result.TerminationReason,
			Timestamp: time.Now().Unix(),
		}
		if appendErr := sess.Append(ctx, marker); appendErr != nil {
			m.rt.logger.Warn("追加子智能体终止标记失败",
				"session", sid, "error", appendErr, "reason", result.TerminationReason)
		} else {
			m.rt.logger.Info("子智能体无最终答案，写入终止标记",
				"session", sid, "reason", result.TerminationReason)
		}
	}

	if err != nil {
		return "", sid, fmt.Errorf("sub-agent %q: %w", agentName, err)
	}

	return result.Answer, sid, nil
}

// registerPermissionWait 登记子会话挂起等待主会话授权的状态。
// 子会话 exec 在 waitForPermissionDecision 挂起时调用，
// 主会话的魔法词解析（dispatchPermission）据此定位目标子会话。
func (m *subAgentManager) registerPermissionWait(sessionID string, ch chan permissionSignal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[sessionID]
	if st == nil {
		return
	}
	st.pendingCh = ch
	st.pendingAt = time.Now().UnixNano()
}

// clearPermissionWait 解除子会话的授权等待登记。
// ch 必须与当前登记的通道一致才生效，避免误清其他 spawn 的登记。
// 注意：本方法不关闭通道——通道关闭统一由 spawn 结束时的 defer 负责，
// 因为同一 spawn 内可能多次挂起等待授权，通道需在整个执行循环期间保持可用。
func (m *subAgentManager) clearPermissionWait(sessionID string, ch chan permissionSignal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[sessionID]
	if st == nil || st.pendingCh != ch {
		return
	}
	st.pendingCh = nil
	st.pendingAt = 0
}

// findPendingSub 返回当前挂起等待授权决策的子会话及其权限通道。
// target 非空时精确匹配指定 sessionID（前端授权弹窗携带 session_id 精确路由，
// 避免多个子会话并发挂起时先到先服务造成决策错位）；为空时按先到先服务
// 选择最早挂起者（与旧行为一致）。
// 返回的通道可能随后被关闭（子会话授权超时/结束），调用方发送前需做好防护。
func (m *subAgentManager) findPendingSub(target string) (string, chan permissionSignal) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if target != "" {
		st := m.states[target]
		if st != nil && st.pendingCh != nil {
			return target, st.pendingCh
		}
		return "", nil
	}
	var (
		earliest int64
		sid      string
		ch       chan permissionSignal
	)
	for id, st := range m.states {
		if st.pendingCh == nil {
			continue
		}
		if earliest == 0 || st.pendingAt < earliest {
			earliest = st.pendingAt
			sid = id
			ch = st.pendingCh
		}
	}
	return sid, ch
}

// dispatchPermission 将主会话（用户）的授权决策路由到挂起等待授权的子会话。
// target 非空时精确路由到指定子会话（前端授权弹窗携带 session_id）；
// 为空时按先到先服务选择最早挂起者。
// 返回 true 表示已成功送达（魔法词已被消费）；false 表示没有可送达的子会话。
func (m *subAgentManager) dispatchPermission(action, target string) bool {
	sid, ch := m.findPendingSub(target)
	if ch == nil {
		return false
	}
	m.rt.logger.Info("向子智能体转发授权决策",
		"session", sid, "action", action, "target", target)
	return safeSendPermission(ch, permissionSignal{action: action})
}

// safeSendPermission 非阻塞发送授权信号到通道。
// 子会话可能在发送前已超时/结束并关闭通道，向已关闭通道发送会 panic，
// 这里用 recover 防护，保证主会话的魔法词解析不因竞态崩溃。
func safeSendPermission(ch chan permissionSignal, sig permissionSignal) (sent bool) {
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	select {
	case ch <- sig:
		return true
	default:
		return false
	}
}

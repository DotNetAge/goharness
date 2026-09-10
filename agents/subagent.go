package agents

import (
	"context"
	"fmt"
	"runtime/debug"
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

	// spawning 是「空闲复用认领」标志：findIdleSession / recoverLatestSession
	// 选中会话时在 m.mu 写锁内置位（探测即认领，原子），spawn 结束时由
	// releaseSpawn 复位。它取代旧的 TryLock「探测后立即解锁」方案——那存在
	// 认领窗口：并发调用方都能通过探测、随后各自加锁，导致多个并行派发
	// 坍缩到同一会话（子任务串行撞车、CollectResults 读串、结果逐字相同）。
	// 显式 sessionID 复用不经此判定（本就是排队语义）；标志泄漏（spawn 在
	// 认领后、接管前失败）的后果仅是该会话不再被空闲复用，不影响正确性。
	spawning bool
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
//   - rt.agentExists：校验子智能体配置是否存在（应用侧回调）
//   - rt.SessionConfigs()：为新建子会话注入 Compactor / Sandbox 等通用能力
//   - rt.logger：日志
type subAgentManager struct {
	rt     *Runtime
	mu     sync.RWMutex
	states map[string]*perSessionState

	// doneChs 是「子任务完成广播」登记表（Promise 语义）：sessionID → 广播通道。
	// spawn 启动时登记、结束时 close（close 即广播，多等待者安全）。
	// CollectResults 经 rt 注入的 waitCompletions 等待通道关闭，
	// 事件驱动感知子任务结束，替代盲轮询（轮询保留为跨进程恢复场景的兜底）。
	doneChs map[string]chan struct{}

	// spawnErrors 记录 spawn 早期失败（无子会话可写终止标记的场景，如
	// getOrCreate 失败）：sessionID → 失败原因。CollectResults 唤醒后
	// 据此直接返回失败，避免对无标记会话死等轮询到 30 分钟上限。
	spawnErrors map[string]error

	// sponsored 是「主会话停止 → 派生子代理级联强停」的登记表：
	// 主会话 ID → (子会话 ID → 子执行循环 ctx 的取消函数)。
	// 子执行循环运行在 Runtime.Ask 新建的独立 Background ctx 上，不随主
	// exec ctx 级联取消（保证工具调用结束、主轮次正常收尾不影响后台子任务），
	// 因此「主会话被停止时强停其全部派生子代理」必须显式登记、显式取消
	// （Runtime.CancelSubAgents）。同一子会话同一时刻至多一个活跃 spawn
	// （per-session 互斥锁保证），登记即覆盖旧句柄；spawn 结束或强停后清理。
	sponsored map[string]map[string]context.CancelFunc
}

// newSubAgentManager 创建子智能体管理器。rt 为所属 Runtime，用于获取编排能力。
func newSubAgentManager(rt *Runtime) *subAgentManager {
	return &subAgentManager{
		rt:          rt,
		states:      make(map[string]*perSessionState),
		doneChs:     make(map[string]chan struct{}),
		spawnErrors: make(map[string]error),
		sponsored:   make(map[string]map[string]context.CancelFunc),
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
	// 显式复用时先查已登记状态：命中则在写锁内置位 spawning 认领标志后直接返回，
	// 避免重复加载。显式复用虽是排队语义，但认领标志必须置位——否则并发派发的
	// findIdleSession（判定条件含 spawning == false）会在本会话显式复用期间把它
	// 当作空闲会话误认领，把本应独立的并行任务串行化到同一会话（上下文混流）。
	// 置位由 spawn 结束时的 releaseSpawn 统一复位（该路径已持锁，直接写安全）。
	if sessionID != "" {
		m.mu.Lock()
		st, ok := m.states[sessionID]
		if ok {
			st.spawning = true
		}
		m.mu.Unlock()
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

	// 双重检查，避免加锁期间其他 goroutine 已登记。命中时同样置位 spawning
	// 认领标志（与上方显式复用快照路径一致，防止被 findIdleSession 误认领），
	// 由 spawn 结束时的 releaseSpawn 统一复位。
	if sessionID != "" {
		if st, ok := m.states[sessionID]; ok {
			st.spawning = true
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
	// spawning 置位贯穿整个 spawn 生命周期：getOrCreate 返回即被本次 spawn
	// 接管（新建分身或显式恢复），直到 spawn 结束由 releaseSpawn 复位。
	// 不能依赖 lastUsedAt 判定活跃——touchSession 在 spawn 开始时即写入时间戳，
	// 若新建会话 spawning 为 false，首个 spawn 运行期间其他并发派发的
	// findIdleSession 会看到「lastUsedAt 非零 && spawning == false」而误认领
	// 正在运行的会话（并行任务坍缩串行，即本次修复的缺陷场景）。
	st := &perSessionState{sess: s, spawning: true}
	m.states[s.ID()] = st
	return st, nil
}

// findIdleSession 在内存已登记会话中查找可复用的空闲会话。
// 匹配条件：AgentName / ProjectDir / Sponsor 三者一致——同一发起方（Sponsor）在
// 同一工作目录（ProjectDir）下安排同一 Agent，讨论的话题相关度高，上下文连续可延续；
// 且会话已被 spawn 认领使用过（lastUsedAt 非零）、当前未被认领（spawning == false）。
// 认领判定与置位同在 m.mu 写锁内完成（探测即认领，原子），并发调用不可能同时
// 选中同一会话；被认领中的会话跳过，由调用方新建分身保持并行。
// 多个候选时选择最近使用（lastUsedAt 最新）的会话。
func (m *subAgentManager) findIdleSession(agentName, projectDir, sponsor string) *perSessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *perSessionState
	for _, st := range m.states {
		if st.sess.AgentName() != agentName ||
			st.sess.ProjectDir() != projectDir ||
			st.sess.Sponsor() != sponsor {
			continue
		}
		if st.lastUsedAt.IsZero() || st.spawning {
			// 从未被 spawn 认领（刚创建/恢复，即将被其 spawn 锁定使用）：
			// 不参与复用，避免并行分身场景下误复用到同一新会话。
			// spawning == true：已被并发调用认领、正在排队或执行中——跳过。
			continue
		}
		if best == nil || st.lastUsedAt.After(best.lastUsedAt) {
			best = st
		}
	}
	if best != nil {
		// 原子认领：本次 spawn 结束后由 releaseSpawn 复位。
		best.spawning = true
	}
	return best
}

// recoverLatestSession 从持久化存储恢复同 Agent + ProjectDir + Sponsor 的最近使用会话，
// 实现跨进程重启后的上下文延续（读取最新会话恢复讨论上下文）。
// 返回 nil 表示存储不可用、无可匹配会话，或候选会话已被并发调用登记/认领（并行分身）。
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

	// 「是否已在内存」检查与登记必须在同一次写锁持有内完成：
	// 旧实现两次独立加锁（RLock 检查 → 解锁 → Load → Lock 登记）存在窗口，
	// 并发调用会在窗口内全部判定「不在内存」而加载同一持久化会话，
	// 登记处双重检查又把它们合并为同一实例——多个并行派发因此共用同一
	// sessionID（子任务串行撞车、结果串台）。写锁内会话加载为本地存储读取，
	// 耗时可接受，正确性优先。
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, inMem := m.states[latest.SessionID]; inMem {
		// 已在内存（含并发调用刚登记的情形）：是否可复用由 findIdleSession
		// 的 spawning 判定，此处一律返回 nil——否则并行分身场景下会误复用
		// 到刚创建、尚未被其 spawn 认领的新会话，导致并行任务被错误串行化。
		return nil
	}

	// 从存储加载历史会话并登记。
	s, err := session.Load(ctx, latest.SessionID, agentName, store, m.rt.logger, m.rt.SessionConfigs()...)
	if err != nil {
		return nil
	}
	st := &perSessionState{sess: s, spawning: true}
	m.states[s.ID()] = st
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

// releaseSpawn 复位会话的「空闲复用认领」标志：spawn 结束（无论成败）时调用，
// 使会话重新参与后续的空闲复用（findIdleSession）。与 findIdleSession /
// recoverLatestSession 的置位配对，同用 m.mu 写锁保证原子性。
// 会话不在登记表时静默忽略（spawn 早期失败尚未来得及登记的场景）。
func (m *subAgentManager) releaseSpawn(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[sessionID]; st != nil {
		st.spawning = false
	}
}

// registerDone 登记子任务完成广播通道（幂等）：已登记则复用既有通道。
// Promise 语义的第一半——spawn 启动即登记，结束（无论成败）由 completeDone
// close 广播。空 sessionID 忽略（无广播对象）。
// 同时清除该会话上一次 spawn 的失败登记：会话复用（同 sid 新任务）场景下，
// 旧失败记录会让 waitCompletions 把本次成功任务误判为启动失败。
func (m *subAgentManager) registerDone(sessionID string) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.doneChs[sessionID]; !ok {
		m.doneChs[sessionID] = make(chan struct{})
	}
	delete(m.spawnErrors, sessionID)
}

// completeDone 广播子任务结束：close 通道唤醒所有等待者并清理登记。
// Promise 语义的第二半——spawn 结束时调用，多等待者（多个 CollectResults）
// 同时唤醒均安全。
func (m *subAgentManager) completeDone(sessionID string) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.doneChs[sessionID]; ok {
		delete(m.doneChs, sessionID)
		close(ch)
	}
}

// failDone 记录 spawn 早期失败并广播结束。
// 早期失败（如 getOrCreate 失败）没有子会话可写终止标记，CollectResults
// 唤醒后查本登记直接返回失败，避免对无标记会话死等轮询到 30 分钟上限。
// 幂等语义限于单次 spawn：保留本次 spawn 的首个失败原因
// （跨 spawn 的旧记录由 registerDone 在新任务登记时清除）。
func (m *subAgentManager) failDone(sessionID string, err error) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.spawnErrors[sessionID]; !ok {
		m.spawnErrors[sessionID] = err
	}
	m.mu.Unlock()
	m.completeDone(sessionID)
}

// waitCompletions 等待指定子任务全部结束（Promise.all 语义）：
// 事件驱动阻塞直至所有会话的 spawn 结束（完成广播通道关闭）或 ctx 取消。
// 无活跃登记的会话视为已完成（跨进程恢复的旧会话无通道，调用方以轮询兜底）。
// 返回本次询问中 spawn 早期失败的 sessionID → 失败原因（正常结束不出现在
// 返回值中，结果由调用方从子会话读取）。
func (m *subAgentManager) waitCompletions(ctx context.Context, sessionIDs []string) map[string]error {
	chs := make(map[string]chan struct{}, len(sessionIDs))
	m.mu.RLock()
	for _, id := range sessionIDs {
		if ch, ok := m.doneChs[id]; ok {
			chs[id] = ch
		}
	}
	m.mu.RUnlock()

	// 等待所有活跃通道关闭：每个通道一个 goroutine，ctx 取消时同步退出。
	if len(chs) > 0 {
		var wg sync.WaitGroup
		for _, ch := range chs {
			wg.Add(1)
			go func(ch chan struct{}) {
				defer wg.Done()
				select {
				case <-ch:
				case <-ctx.Done():
				}
			}(ch)
		}
		wg.Wait()
	}

	// 等待结束后查失败登记：此时 failDone 必然已完成，不会漏掉等待期间的失败。
	errs := make(map[string]error)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range sessionIDs {
		if e, ok := m.spawnErrors[id]; ok {
			errs[id] = e
		}
	}
	return errs
}

// registerSponsored 登记运行中子代理的强停句柄：sponsorSessionID 为主会话 ID，
// subSessionID 为子会话 ID，cancel 为子执行循环 ctx 的取消函数。
// 同一子会话同一时刻至多一个活跃 spawn（per-session 互斥锁保证），重复登记
// 即覆盖旧句柄；空主会话 ID 或空取消函数忽略（无强停入口）。
func (m *subAgentManager) registerSponsored(sponsorSessionID, subSessionID string, cancel context.CancelFunc) {
	if sponsorSessionID == "" || cancel == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	subs, ok := m.sponsored[sponsorSessionID]
	if !ok {
		subs = make(map[string]context.CancelFunc)
		m.sponsored[sponsorSessionID] = subs
	}
	subs[subSessionID] = cancel
}

// unregisterSponsored 注销强停句柄：spawn 结束（无论成败）时调用。
// 句柄不存在时静默返回（cancelSponsored 强停时可能已提前清理）。
func (m *subAgentManager) unregisterSponsored(sponsorSessionID, subSessionID string) {
	if sponsorSessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	subs, ok := m.sponsored[sponsorSessionID]
	if !ok {
		return
	}
	delete(subs, subSessionID)
	if len(subs) == 0 {
		delete(m.sponsored, sponsorSessionID)
	}
}

// cancelSponsored 强停指定主会话派生的全部运行中子代理：
// 逐一取消其执行循环 ctx（LLM 流式调用、工具执行、授权挂起等待均随 ctx
// 取消而终止；子会话随后写入终止标记，CollectResults 可立即感知失败），
// 并清理登记避免重复强停。返回被强停的子代理数。
func (m *subAgentManager) cancelSponsored(sponsorSessionID string) int {
	if sponsorSessionID == "" {
		return 0
	}
	m.mu.Lock()
	subs, ok := m.sponsored[sponsorSessionID]
	if !ok {
		m.mu.Unlock()
		return 0
	}
	cancels := make([]context.CancelFunc, 0, len(subs))
	for _, cancel := range subs {
		cancels = append(cancels, cancel)
	}
	delete(m.sponsored, sponsorSessionID)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
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
	// panic 兜底（覆盖早期阶段：agent 校验、上下文检查、会话定位）：
	// spawn 链路任何 panic 不得击穿到 tools 层后台 goroutine 崩掉整个进程。
	// 捕获后转为错误返回，并登记失败广播——CollectResults 唤醒后查 spawnErrors
	// 直接感知失败，不至对无终止标记的子会话死等轮询。此时完成广播通道可能尚未
	// 登记（failDone 的 close 为 no-op），感知依赖 spawnErrors 查询：CollectResults
	// 调用必然晚于 SubAgent 工具返回，而后者必然晚于本函数返回（failDone 已完成），
	// 时序严格成立、无竞态。sessionID 非空时顺带复位 spawning（releaseSpawn defer
	// 在早期 panic 场景尚未注册）；为空时无法定位会话，后果仅是该会话不再参与
	// 空闲复用，不影响正确性。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("子智能体执行发生 panic: %v", r)
			m.rt.logger.Error("子智能体执行 panic",
				err,
				"agent_name", agentName,
				"session_id", sessionID,
				"stack", string(debug.Stack()),
			)
			m.failDone(sessionID, err)
			if sessionID != "" {
				m.releaseSpawn(sessionID)
			}
		}
	}()

	// Agent 存在性校验走应用侧回调（goharness 对 Agent 结构零依赖）。
	if m.rt.agentExists != nil && !m.rt.agentExists(agentName) {
		spawnErr := fmt.Errorf("未找到智能体配置: %q", agentName)
		// sessionID 已知（ensureSession 存根）时登记失败并广播，
		// CollectResults 唤醒后直接拿到失败原因，不至死等轮询。
		m.failDone(sessionID, spawnErr)
		return "", "", spawnErr
	}

	tc := tools.GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		spawnErr := fmt.Errorf("上下文未包含会话")
		m.failDone(sessionID, spawnErr)
		return "", "", spawnErr
	}
	// 完成广播登记（Promise 语义）：sessionID 已知（ensureSession 存根/显式复用）
	// 时立即登记，使 getOrCreate 失败也能广播；为空（新建/复用空闲会话）时在
	// 会话确定后补登记。spawn 结束（无论成败）统一 close 广播，
	// CollectResults 事件驱动感知子任务结束，替代盲轮询。
	if sessionID != "" {
		m.registerDone(sessionID)
	}
	st, err := m.getOrCreate(ctx, agentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store(), sessionID)
	if err != nil {
		wrapped := fmt.Errorf("获取子智能体会话失败: %q: %w", agentName, err)
		m.failDone(sessionID, wrapped)
		return "", "", wrapped
	}
	sess := st.sess
	if sessionID == "" {
		sessionID = sess.ID()
		m.registerDone(sessionID)
	}
	defer m.completeDone(sessionID)

	// per-session 互斥：同一会话的并发 Ask 串行执行。
	// 锁粒度 = 完整 exec 循环，持有期间阻塞其他对该会话的 spawn。
	st.lock.Lock()
	defer st.lock.Unlock()

	// 认领释放：spawn 结束（无论成败）后复位 spawning 标志，使会话重新参与
	// 后续的空闲复用。defer 栈上位于 st.lock.Unlock 之前执行（LIFO），
	// 复位动作本身走 m.mu 写锁，与 per-session 互斥锁无耦合。
	defer m.releaseSpawn(sess.ID())

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

	// 强停登记（级联取消锚点）：以发起方主会话 ID 为键登记子执行循环的
	// 取消函数，主会话被停止时由宿主（mindx）经 Runtime.CancelSubAgents
	// 统一强停。登记在 per-session 锁持有期间进行，保证同一子会话同一
	// 时刻至多一个活跃登记；spawn 结束（无论成败）注销。
	m.registerSponsored(tc.Session.ID(), sess.ID(), builder.cancel)
	defer m.unregisterSponsored(tc.Session.ID(), sess.ID())

	// panic 兜底（覆盖执行循环阶段）：捕获后转为错误返回并登记失败广播。
	// defer LIFO 顺序保证本 recover 最先执行——failDone 先写入 spawnErrors 并
	// close 广播唤醒等待者，随后函数尾部 defer completeDone 发现键已被清理而
	// no-op，确保「等待者唤醒 → 查 spawnErrors」必然读到失败原因，无竞态窗口。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("子智能体执行发生 panic: %v", r)
			m.rt.logger.Error("子智能体执行 panic",
				err,
				"agent_name", agentName,
				"session_id", sessionID,
				"stack", string(debug.Stack()),
			)
			m.failDone(sessionID, err)
		}
	}()

	result, err := builder.Run()

	sid = sess.ID()

	// 子会话无最终答案的非正常终止（授权超时、上下文取消、执行错误等）：
	// 追加终止标记，供 CollectResults 快速判定失败，避免轮询死等到默认 30 分钟超时。
	// 错误场景同样写入标记（如 llm_error / cancelled），否则 CollectResults 无法区分
	// "子会话还在运行" 与 "已静默终止"，会死等轮询。
	//
	// 例外：AskUser 阻塞（ask_user_pending）不追加标记——子会话并非失败终止，
	// 而是在等待用户在子会话 Tab 回答（用户回答后由 daemon 启动下一次 exec 恢复循环）。
	// 若追加标记，CollectResults 的 findFinalAnswer 会把它识别为终止原因并立即判定失败，
	// 而子会话实际还在等待回答，导致主 Agent 误判子任务失败。
	// 不追加标记时 findFinalAnswer 从后向前扫描到任务的 user 边界即返回空，继续轮询，
	// 直到用户回答后的最终答案出现。
	if result.Answer == "" && result.TerminationReason != "ask_user_pending" {
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

package agents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/DotNetAge/goagent"
	"github.com/DotNetAge/goagent/subagent"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// parentEmitKeyType 用于在 context 中传递父级 EventBus 发射器，
// 使子智能体能够将事件转发到父级事件总线。
type parentEmitKeyType struct{}

// subagentSem 是子代理任务的并发信号量。
// 限制同时运行的子代理任务数量，防止资源耗尽；满载时新任务保持
// Pending（已受理未运行），有槽位释放后自动开跑。
var subagentSem = make(chan struct{}, 20)

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

	// pendingAskCh 是子智能体 AskUser 提问冒泡的等待通道（镜像 pendingCh）。
	// 子会话 exec 调用 AskUser 挂起等待回答时由 registerAskWait 登记，
	// daemon 收到用户回答后经 dispatchAskAnswer 把答案送入该通道；
	// exec 结束后由 clearAskWait 清除并关闭。nil 表示未挂起等待回答。
	pendingAskCh chan string

	// pendingAskAt 记录子会话开始挂起等待提问回答的纳秒时间戳。
	// 多个子会话同时挂起提问时，未指定目标的回答按此时间戳先到先服务。
	pendingAskAt int64

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

// subAgentManager 管理子智能体的会话登记与派发执行，是 Runtime 的一个内聚子系统，
// 从 Runtime 抽离以减轻后者的职责密度。它同时是 goagent/subagent.SubAgentDispatcher
// 的宿主实现：Submit 受理派发请求（登记控制平面 + 发射受理事件）并立即返回回执，
// 子任务在后台 goroutine 中独立运行，结果经控制平面（RuntimeManager）按句柄结算。
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
		rt:        rt,
		states:    make(map[string]*perSessionState),
		sponsored: make(map[string]map[string]context.CancelFunc),
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

// cancelAllSponsored 强停全部主会话派生的运行中子代理（不限 sponsor），
// 供宿主停机安全网使用（Runtime.CancelAllSubAgents）。语义与 cancelSponsored
// 一致：逐一取消子执行循环 ctx、清理登记；子会话随后由 spawn 的 defer 写入
// 终止标记。返回被强停的子代理数。
func (m *subAgentManager) cancelAllSponsored() int {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sponsored))
	for sponsor, subs := range m.sponsored {
		for _, cancel := range subs {
			cancels = append(cancels, cancel)
		}
		delete(m.sponsored, sponsor)
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// Submit 受理子任务派发（实现 goagent/subagent.SubAgentDispatcher）。
// 同步完成「校验 → 会话定位 → 控制平面登记 → 受理事件发射」，立即返回回执；
// 子任务在后台 goroutine 中独立运行（runTask），结果经控制平面按跟踪句柄结算。
// 本方法只做受理，不等待任务执行——CollectResults 经回执中的 TaskID 主动读取结果。
//
// 设计决策：
//   - agent 配置不存在的柔性校验前移到受理阶段：拒绝以回执（Accepted=false + Reason）
//     返回，LLM 本轮即可自纠，不产生无效任务条目。
//   - 会话定位（空闲复用 + 并行分身 + 显式复用）在同步阶段完成：spawning 认领标志
//     即刻置位，保证并发派发不坍缩到同一会话；子会话 ID 随受理事件同步发射给前端。
//   - 控制平面登记即受理：任务条目以 Pending 态进入 RuntimeManager（UI 立即可见），
//     后台 goroutine 运行完毕后经 Complete/Fail 结算（终态条目保留供 CollectResults
//     重试查询，不做 Unregister）。
func (m *subAgentManager) Submit(ctx context.Context, req subagent.SubAgentRequest) (receipt subagent.SubAgentReceipt, err error) {
	// acceptedTaskID 非空表示控制平面条目已注册、但后台 goroutine 尚未启动：
	// 此窗口内 panic 时条目无人结算，defer 中补偿 Fail（否则 CollectResults
	// 对该句柄死等 Done 永不关闭）；goroutine 启动后置空，结算由 runTask 保证。
	var acceptedTaskID string

	// panic 兜底：受理链路（agentExists 应用侧回调、会话定位等）任何 panic 不得
	// 击穿工具调用崩掉主会话执行循环，捕获后转为错误返回。
	defer func() {
		if r := recover(); r != nil {
			if acceptedTaskID != "" {
				goagent.DefaultRuntimeManager().Fail(acceptedTaskID, fmt.Sprintf("派发受理中断: %v", r))
			}
			receipt = subagent.SubAgentReceipt{}
			err = fmt.Errorf("派发子智能体任务时发生 panic: %v", r)
			m.rt.logger.Error("派发子智能体任务 panic", err,
				"agent_name", req.AgentName,
				"stack", string(debug.Stack()),
			)
		}
	}()

	// Agent 存在性校验走应用侧回调（goharness 对 Agent 结构零依赖）。
	// 策略性拒绝：经回执返回原因（非 error），与 goagent/subagent 协议语义一致。
	if m.rt.agentExists != nil && !m.rt.agentExists(req.AgentName) {
		return subagent.SubAgentReceipt{
			Accepted: false,
			Reason:   fmt.Sprintf("系统中不存在名为 %q 的智能体。请改用系统提示词智能体清单中的名称重新派发；若要创建自己的分身，agent_name 传你自己的名称", req.AgentName),
		}, nil
	}

	tc := tools.GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return subagent.SubAgentReceipt{}, fmt.Errorf("上下文未包含会话")
	}

	// 会话定位（同步阶段）：空闲复用 / 并行分身 / 显式复用的认领即在此完成，
	// 子会话 ID 随受理事件发射，前端立即可见派发目标。
	st, err := m.getOrCreate(ctx, req.AgentName, tc.Session.ProjectDir(), tc.Session.AgentName(), tc.Session.Store(), req.SessionID)
	if err != nil {
		return subagent.SubAgentReceipt{}, fmt.Errorf("获取子智能体会话失败: %q: %w", req.AgentName, err)
	}
	sid := st.sess.ID()

	// 受理即登记控制平面（Pending 态，UI 立即可见）；TaskID 作为跟踪句柄
	// 写入回执，CollectResults 据此等待并读取结果。
	taskID := newTaskID()
	if _, err := goagent.DefaultRuntimeManager().Register(taskID); err != nil {
		return subagent.SubAgentReceipt{}, fmt.Errorf("登记子任务运行实例失败: %w", err)
	}
	acceptedTaskID = taskID

	// 受理事件：前端据此渲染「子任务运行中」状态（不受任务实例 Pending 态影响）。
	if tc.EmitEvent != nil {
		tc.EmitEvent(events.ReactEvent{
			AgentName: req.AgentName,
			Type:      events.SubtaskSpawned,
			Data: events.SubtaskInfo{
				AgentName:   req.AgentName,
				Description: req.Task,
				SessionID:   sid,
				TaskID:      taskID,
			},
		})
	}

	m.rt.logger.Info("sub-agent task accepted",
		"agent_name", req.AgentName,
		"session_id", sid,
		"task_id", taskID,
	)

	// 后台执行：goroutine 归宿主，ctx 剥离取消信号与截止时间（Execute 同步返回后
	// execCtx 会被取消，会话持久化操作不得随之中断）、保留 Value（logger /
	// ToolContext / 授权与提问 sink）。goroutine 一经启动即由 runTask 保证结算，
	// 补偿窗口关闭。
	acceptedTaskID = ""
	go m.runTask(context.WithoutCancel(ctx), taskID, req.AgentName, req.Task, st, tc)

	return subagent.SubAgentReceipt{Accepted: true, TaskID: taskID}, nil
}

// runTask 在后台 goroutine 中运行子任务并结算控制平面条目。
// 执行完毕后经 Complete（成功）/ Fail（失败）写入终态并关闭 Done，
// 等待中的 CollectResults 随即被唤醒；同时发射 SubtaskCompleted 事件。
// 本函数承载既有 spawn 的全部编排语义：subagentSem 并发上限、per-session
// 互斥、认领释放、授权与提问冒泡通道、sponsored 强停登记。
func (m *subAgentManager) runTask(ctx context.Context, taskID, agentName, task string, st *perSessionState, tc *tools.ToolContext) {
	manager := goagent.DefaultRuntimeManager()
	sess := st.sess
	startedAt := time.Now()

	// settled 标记结算已完成（Fail/Complete 已执行）；其后链路（日志 / 完成事件
	// 回调）panic 时仅记日志，不再补偿结算。
	settled := false

	// panic 兜底：执行闭包自行 recover 转错误，但结算链路（logger / EmitEvent
	// 外部回调）仍可能 panic——任何 panic 不得击穿后台 goroutine 崩掉整个进程；
	// 未结算时补偿 Fail，防止 CollectResults 对该句柄死等 Done 永不关闭。
	defer func() {
		if r := recover(); r != nil {
			if !settled {
				manager.Fail(taskID, fmt.Sprintf("子任务执行链路 panic: %v", r))
			}
			m.rt.logger.Error("子任务 goroutine panic",
				fmt.Errorf("%v", r),
				"agent_name", agentName,
				"session_id", sess.ID(),
				"task_id", taskID,
				"stack", string(debug.Stack()),
			)
		}
	}()

	// 并发上限：信号量在后台 goroutine 中获取，满载时任务保持 Pending
	//（已受理未运行，控制平面立即可见），有槽位释放后自动开跑。
	subagentSem <- struct{}{}
	defer func() { <-subagentSem }()

	// 单轮执行闭包：返回答案、终止原因与错误；panic 统一转错误。
	answer, termReason, execErr := func() (answer, termReason string, err error) {
		// panic 兜底：执行链路任何 panic 不得击穿后台 goroutine 崩掉整个进程，
		// 捕获后按失败结算控制平面条目。
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("子智能体执行发生 panic: %v", r)
				m.rt.logger.Error("子智能体执行 panic",
					err,
					"agent_name", agentName,
					"session_id", sess.ID(),
					"task_id", taskID,
					"stack", string(debug.Stack()),
				)
			}
		}()

		// per-session 互斥：同一会话的并发 Ask 串行执行，从根上杜绝消息交错
		// （assistant tool_calls → tool 配对被并发写破坏）；不同会话之间完全并行。
		st.lock.Lock()
		defer st.lock.Unlock()

		// 认领释放：任务结束（无论成败）后复位 spawning 标志，使会话重新参与
		// 后续的空闲复用。复位动作走 m.mu 写锁，与 per-session 互斥锁无耦合。
		defer m.releaseSpawn(sess.ID())

		// 记录本次使用时间：后续同一 Agent + ProjectDir + Sponsor 的派发
		// 可据此判断空闲会话并复用（延续讨论上下文）。
		m.touchSession(sess.ID())

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
		// 子智能体提问冒泡旁路（镜像授权旁路）：从 ctx 读取 AskUser 提问直达
		// 前端的发送器并注入 builder。ctx 中的值由宿主经 WithAskSink 注入；
		// 未注入时提问退回原 parentEmit 转发链路，行为保持不变。
		if askSink, ok := ctx.Value(askSinkKeyType{}).(AskUserSink); ok {
			builder.askSink = askSink
		}
		// 子智能体授权冒泡：创建权限信号通道并注入 builder。
		// 子会话 exec 遇到需要授权的工具时通过该通道挂起等待主会话（用户）的授权决策，
		// 授权后继续执行；超时则以 permission_timeout 终止。
		permissionCh := make(chan permissionSignal, 1)
		builder.permissionCh = permissionCh
		// 通道生命周期随本任务结束而结束：解除挂起登记并关闭通道。
		// waitForPermissionDecision 每次挂起/恢复也会解除登记，但不关闭通道——
		// 同一任务内可能多次触发授权（授权后继续循环又遇到需授权的工具），
		// 通道必须保持可用，直到整个执行循环结束。
		defer func() {
			m.clearPermissionWait(sess.ID(), permissionCh)
			close(permissionCh)
		}()

		// 子智能体提问冒泡（镜像授权通道）：创建提问回答通道并注入 builder。
		// 子会话 exec 调用 AskUser 时通过该通道挂起等待用户回答，
		// 回答注入后继续执行；超时则以 ask_timeout 终止。
		askCh := make(chan string, 1)
		builder.askCh = askCh
		// 通道生命周期随本任务结束而结束（镜像授权通道）：解除挂起登记并关闭。
		defer func() {
			m.clearAskWait(sess.ID(), askCh)
			close(askCh)
		}()

		// 强停登记（级联取消锚点）：以发起方主会话 ID 为键登记子执行循环的
		// 取消函数，主会话被停止时由宿主（mindx）经 Runtime.CancelSubAgents
		// 统一强停。登记在 per-session 锁持有期间进行，保证同一子会话同一
		// 时刻至多一个活跃登记；任务结束（无论成败）注销。
		m.registerSponsored(tc.Session.ID(), sess.ID(), builder.cancel)
		defer m.unregisterSponsored(tc.Session.ID(), sess.ID())

		result, runErr := builder.Run()
		if runErr != nil {
			return "", result.TerminationReason, fmt.Errorf("sub-agent %q: %w", agentName, runErr)
		}
		return result.Answer, result.TerminationReason, nil
	}()

	// 结算控制平面条目（终态写入 + close Done 唤醒等待者）：
	//   - 执行错误 → Failed（携带原因）；
	//   - 无最终答案的非正常终止（授权超时、执行取消等）→ Failed；
	//     例外 ask_user_pending（等待用户作答的特殊路径）与空终止原因按完成结算。
	//   - 正常 → Completed（结果经 Runtime.Result() 供 CollectResults 读取）。
	// 终态条目保留在登记表中（不 Unregister）：CollectResults 重试查询与
	// 「刚刚结束」窗口都依赖它，清理交给宿主策略。
	failureReason := ""
	switch {
	case execErr != nil:
		failureReason = execErr.Error()
	case answer == "" && termReason != "" && termReason != "ask_user_pending":
		failureReason = termReason
	}
	if failureReason != "" {
		manager.Fail(taskID, failureReason)
	} else {
		manager.Complete(taskID, answer)
	}
	settled = true

	elapsed := time.Since(startedAt)
	if failureReason != "" {
		m.rt.logger.Error("sub-agent task failed",
			fmt.Errorf("%s", failureReason),
			"agent_name", agentName,
			"session_id", sess.ID(),
			"task_id", taskID,
			"elapsed_ms", elapsed.Milliseconds(),
		)
	} else {
		m.rt.logger.Info("sub-agent task completed",
			"agent_name", agentName,
			"session_id", sess.ID(),
			"task_id", taskID,
			"elapsed_ms", elapsed.Milliseconds(),
			"result_len", len(answer),
		)
	}

	// 完成事件：前端据此更新子任务状态（含跟踪句柄与结果摘要）。
	if tc.EmitEvent != nil {
		tc.EmitEvent(events.ReactEvent{
			AgentName: agentName,
			Type:      events.SubtaskCompleted,
			Data: events.SubtaskResult{
				AgentName:   agentName,
				Success:     failureReason == "",
				Answer:      answer,
				Error:       failureReason,
				Description: task,
				SessionID:   sess.ID(),
				TaskID:      taskID,
			},
		})
	}
}

// newTaskID 生成随机任务跟踪句柄（受理回执返回给 LLM，CollectResults 据此查询）。
func newTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见；退化为时间戳保证非空唯一性
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return "task-" + hex.EncodeToString(b[:])
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

// registerAskWait 登记子会话挂起等待用户回答提问的状态（镜像 registerPermissionWait）。
// 子会话 exec 在 waitForAskUserDecision 挂起时调用，
// daemon 收到用户回答时据此定位目标子会话并路由答案。
func (m *subAgentManager) registerAskWait(sessionID string, ch chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[sessionID]
	if st == nil {
		return
	}
	st.pendingAskCh = ch
	st.pendingAskAt = time.Now().UnixNano()
}

// clearAskWait 解除子会话的提问等待登记（镜像 clearPermissionWait）。
// ch 必须与当前登记的通道一致才生效，避免误清其他 spawn 的登记。
// 注意：本方法不关闭通道——通道关闭统一由 spawn 结束时的 defer 负责，
// 因为同一 spawn 内可能多次挂起等待提问，通道需在整个执行循环期间保持可用。
func (m *subAgentManager) clearAskWait(sessionID string, ch chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[sessionID]
	if st == nil || st.pendingAskCh != ch {
		return
	}
	st.pendingAskCh = nil
	st.pendingAskAt = 0
}

// findPendingAskTarget 返回当前挂起等待提问回答的子会话 ID 及其通道。
// target 非空时精确匹配指定 sessionID（前端作答携带 session_id 精确路由，
// 避免多个子会话并发提问时回答错位）；为空时按先到先服务选择最早挂起者。
// 返回的通道可能随后被关闭（子会话提问超时/结束），调用方发送前需做好防护。
func (m *subAgentManager) findPendingAskTarget(target string) (string, chan string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if target != "" {
		st := m.states[target]
		if st != nil && st.pendingAskCh != nil {
			return target, st.pendingAskCh
		}
		return "", nil
	}
	var (
		earliest int64
		sid      string
		ch       chan string
	)
	for id, st := range m.states {
		if st.pendingAskCh == nil {
			continue
		}
		if earliest == 0 || st.pendingAskAt < earliest {
			earliest = st.pendingAskAt
			sid = id
			ch = st.pendingAskCh
		}
	}
	return sid, ch
}

// dispatchAskAnswer 将用户对子代理提问的回答路由到挂起等待的子会话。
// target 非空时精确路由到指定子会话（前端作答携带 session_id）；
// 为空时按先到先服务选择最早挂起者。
// 返回 true 表示已成功送达（回答将被注入子会话并恢复其循环）；
// false 表示没有挂起提问的子会话（调用方按普通消息处理）。
func (m *subAgentManager) dispatchAskAnswer(target, answer string) bool {
	sid, ch := m.findPendingAskTarget(target)
	if ch == nil {
		return false
	}
	m.rt.logger.Info("向子智能体转发用户回答",
		"session", sid, "target", target, "answer_len", len(answer))
	// 非阻塞发送 + recover 防护：子会话可能在发送前已超时/结束并关闭通道，
	// 与 safeSendPermission 相同的竞态防护，保证路由方不因竞态崩溃。
	defer func() { _ = recover() }()
	select {
	case ch <- answer:
		return true
	default:
		return false
	}
}

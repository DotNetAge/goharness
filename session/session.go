// Package session 为 AI 智能体提供对话会话管理。
// 它处理消息存储、上下文窗口管理和自动压缩，以防止长对话中的 token 限制耗尽。
//
// 关键特性：
//   - 线程安全的消息追加和检索
//   - 基于 token 计数的自动上下文窗口压缩
//   - 可配置的窗口大小和摘要
//   - 持久化存储后端支持
//   - 上下文摘要的记忆存储
//
// 架构：
//
//	┌─────────────┐     ┌────────────────┐     ┌──────────────┐
//	│  Session    │────>│  SessionStore  │────>│  Persistence │
//	│  (in-memory)│     │  (interface)   │     │  (disk/db)   │
//	└─────────────┘     └────────────────┘     └──────────────┘
//	      │                    │
//	      ▼                    ▼
//	┌─────────────┐     ┌────────────────┐
//	│  MemoryStore│     │   Summarizer   │
//	│  (summaries)│     │  (LLM calls)   │
//	└─────────────┘     └────────────────┘
//
// 用法：
//
//	session, err := session.New("agent-name", "", "/home/user/project", store)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	session.Append(ctx, session.Message{
//	    Role:    "user",
//	    Content: "Hello!",
//	})
//	current := session.Current()
package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/oklog/ulid/v2"
)

// initSession 执行通用的会话初始化逻辑。
// 提取出来避免 New() 和 Load() 重复代码。
func initSession(id, agentName, sponsor, projectDir string, store SessionStore, logger logging.Logger, opts ...SessionConfig) *Session {
	s := &Session{
		id:         id,
		agentName:  agentName,
		sponsor:    sponsor,
		projectDir: projectDir,
		messages:   make([]Message, 0),
		store:      store,
		log:        logger,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// New 创建一个具有自动生成 ULID 的新对话会话。
// 这是模式 1（"全新 Session"）构造：
//   - AgentName(Owner): 操作此会话的智能体（必填）
//   - Sponsor: 创建/发起此会话的智能体（必填，空字符串表示用户发起）
//   - ProjectDir: 文件操作的工作目录（必填）
//   - ID: 自动生成的 ULID
//
// 参数：
//   - agentName: 操作此会话的智能体名称（必填，不能为空）
//   - sponsor: 创建/发起此会话的智能体（必填，空字符串表示用户发起）
//   - projectDir: 文件操作的工作目录（必填，不能为空）
//   - store: 用于持久化的 SessionStore 实现（必填，不能为 nil）
//   - logger: 用于会话操作的日志记录器（必填，不能为 nil）
//   - opts: 可选的配置函数（WithMemory 等）
//
// 返回：
//   - 一个完全初始化、可接收消息的 Session
//   - 如果任何必填参数缺失，则返回错误
//
// 线程安全：对返回的 Session 的所有操作都是线程安全的。
func New(agentName, sponsor, projectDir string, store SessionStore, logger logging.Logger, opts ...SessionConfig) (*Session, error) {
	if agentName == "" {
		return nil, fmt.Errorf("创建会话失败: agentName 不能为空")
	}
	if projectDir == "" {
		return nil, fmt.Errorf("创建会话失败: projectDir 不能为空")
	}
	if store == nil {
		return nil, fmt.Errorf("创建会话失败: store 不能为 nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("创建会话失败: logger 不能为 nil")
	}
	entropy := ulid.Monotonic(rand.Reader, 0)
	id := ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
	return initSession(id.String(), agentName, sponsor, projectDir, store, logger, opts...), nil
}

// Load 使用持久化存储从现有会话 ID 重建 Session。
//
// 这是模式 2（"从 ID 中加载"）构造：
//   - ctx: 用于控制超时和取消的上下文
//   - sessionID: 现有会话的 ULID（必填，不能为空）
//   - agentName: 操作此会话的智能体名称（可选，可以为空以从元数据恢复）
//   - store: 要加载的持久化 SessionStore（必填，不能为 nil）
//   - logger: 用于会话操作的日志记录器（必填，不能为 nil）
//   - opts: 可选的配置函数（WithMemory、WithSummarizer 等）
//
// Load 通过调用 store.GetMeta() 验证会话是否存在。如果会话
// 在存储中未找到，则返回错误 — 调用者应将其作为"会话不存在"处理。
//
// 如果 agentName 为空，它将从存储的会话元数据中恢复。
// ProjectDir 自动从存储的会话元数据中恢复。
// Sponsor 从存储的会话元数据中恢复（如果之前已持久化）。
func Load(ctx context.Context, sessionID, agentName string, store SessionStore, logger logging.Logger, opts ...SessionConfig) (*Session, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("加载会话失败: sessionID 不能为空")
	}
	if store == nil {
		return nil, fmt.Errorf("加载会话失败: store 不能为 nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("加载会话失败: logger 不能为 nil")
	}

	info, err := store.GetMeta(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("会话 %q 未找到: %w", sessionID, err)
	}

	// 如果调用方未提供 agentName，从元数据中恢复
	if agentName == "" {
		agentName = info.AgentName
	}

	return initSession(sessionID, agentName, info.Sponsor, info.ProjectDir, store, logger, opts...), nil
}

// Session 管理对话的消息历史，具有自动压缩功能。
// 它提供对消息的线程安全访问，并实现滑动窗口模式
// 以管理 LLM token 限制内的上下文长度。
//
// 内部结构：
//
//	┌─────────────────────────────────────────┐
//	│              messages[]                  │
//	├──────────────────┬──────────────────────┤
//	│  [0..cursor]     │  [cursor..len]       │
//	│  Historical      │  Active Window       │
//	│  (compacted)     │  (sent to LLM)       │
//	└──────────────────┴──────────────────────┘
//
// 线程安全：所有公共方法都可以安全地并发使用。
type Session struct {
	mu sync.RWMutex

	// id 是唯一会话标识符
	id string

	// agentName 标识哪个智能体拥有此会话
	agentName string

	// sponsor 标识创建/发起此会话的智能体。
	// 空表示用户发起（智能体 ↔ 用户对话）。
	// 非空表示智能体生成（来自另一个智能体的 SubAgent）。
	sponsor string

	// projectDir 是文件操作的工作目录
	projectDir string

	// modelContextResolver 返回当前会话使用的模型的上下文长度（ContextLength）。
	// 每次需要窗口大小时动态调用，保证切换模型后立即生效。
	// 为 nil 时返回 0（禁用压缩），与旧行为一致。
	modelContextResolver func() int64

	// cursor 分隔历史消息和活跃窗口
	cursor int

	// messages 存储所有消息（历史 + 活跃）
	// 这是一个懒加载缓存：首次访问前为空，然后从存储加载
	messages []Message

	// store 提供持久化消息存储
	store SessionStore

	// log 是会话操作的结构化日志记录器。
	log logging.Logger

	// summarizer 在压缩期间生成摘要
	summarizer Summarizer

	// mem 存储上下文摘要以供后续检索
	mem MemoryStore

	// compactionHandler 在每次压缩事件后调用
	compactionHandler func(CompactionEvent)

	// compactStartHandler 在 TryCompact 开始压缩前调用。
	compactStartHandler func(windowTokens int64, maxWindowSize int64)

	// compactDoneHandler 在 TryCompact 完成后调用。
	compactDoneHandler func(messagesSlid int, windowTokens int64)

	// microCompactStartHandler 在 TryMicroCompact 开始前调用。
	microCompactStartHandler func(windowTokens int64, maxWindowSize int64)

	// microCompactDoneHandler 在 TryMicroCompact 完成后调用。
	microCompactDoneHandler func(compressed, deduped int, windowTokens int64)

	// loaded 指示消息是否已从持久化存储加载。
	// 当为 false 时，Current() 和 Append() 将触发自动懒加载。
	loaded bool

	// loadingMu 防止并发懒加载操作
	loadingMu sync.Mutex

	// modifyFiles 追踪在此会话期间已修改的文件路径。
	// 每个条目是已写入/编辑的文件的绝对路径，
	// 备份存储在会话的备份目录中。
	modifyFiles []string

	// fileModifyHandler 在文件被追踪、确认或回滚时调用。
	fileModifyHandler FileModifyHandler

	// pendingPermission 存储当前等待用户授权的工具调用。
	// 当实现 PermissionRequired.Grant 的工具返回 granted=false 时，
	// 由运行时设置，并在用户在后续 Ask 调用中响应
	// PermissionAllow / PermissionDeny 魔法词时清除。
	//
	// 存储在会话上（而不是运行时上）使权限流能够在 Ask() 调用
	// 和子智能体边界之间存活，因为会话是共享状态。
	pendingPermission *PendingPermission
	pendingMu         sync.Mutex

	// whitelist 是会话级工具白名单的内存缓存。
	// 首次访问时从 {SessionDir()}/session-wl.json 懒加载。
	whitelist   *SessionWhitelist
	whitelistMu sync.Mutex

	// sandbox 是会话级逻辑沙箱，提供统一的文件/网络/命令安全决策。
	// 为 nil 时表示未启用沙箱，工具回退到各自的安全检查逻辑（向后兼容）。
	// 通过 WithSandbox Option 注入。
	sandbox *sandbox.Sandbox
}

// ID 返回此会话的唯一标识符。
func (s *Session) ID() string { return s.id }

// AgentName 返回操作此会话的智能体名称。
func (s *Session) AgentName() string { return s.agentName }

// ProjectDir 返回与此会话关联的工作目录。
func (s *Session) ProjectDir() string { return s.projectDir }

// Sandbox 返回会话级逻辑沙箱实例。
// 返回 nil 表示未启用沙箱，调用方应回退到各自的安全检查逻辑。
func (s *Session) Sandbox() *sandbox.Sandbox { return s.sandbox }

// Sponsor 返回创建/发起此会话的智能体名称。
// 对于用户发起的会话返回空字符串。
func (s *Session) Sponsor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sponsor
}

// ModelContextLength 返回当前会话使用的模型的上下文窗口大小（以 token 为单位）。
//
// 每次调用都通过 modelContextResolver 回调从当前默认模型动态读取，
// 保证用户切换模型后立即生效——这是修复"窗口大小焊死在 session 上"
// 设计 bug 的核心：窗口大小是模型能力的函数，不是会话的固定属性。
//
// 回调未注入或返回 0 时禁用自动压缩，与旧 maxWindowSize=0 行为一致。
func (s *Session) ModelContextLength() int64 {
	if s.modelContextResolver == nil {
		return 0
	}
	return s.modelContextResolver()
}

// CurrentWindowTokens 使用与 MicroCompact/TryMicroCompact 相同的基于 DeepSeek 的公式
// 估算活跃窗口（messages[cursor:]）的 token 数。
func (s *Session) CurrentWindowTokens() int64 {
	return s.ContextUsage().WindowTokens
}

// ContextUsage 返回当前上下文窗口使用信息，
// 使用与 MicroCompact/TryMicroCompact 相同的 token 估算方法。
// 如果定价非 nil，则从每条消息的 Usage 数据计算 TotalCost。
func (s *Session) ContextUsage(pricing ...PricingUnit) ContextWindowUsage {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()

	window := s.messages[s.cursor:]
	windowTokens := estimateWindowTokensV2(window)
	mws := s.ModelContextLength()

	var ratio float64
	if mws > 0 {
		ratio = float64(windowTokens) / float64(mws)
	}

	// 从有 Usage 数据的活跃窗口计算总计
	var totalActual int64
	var totalCost float64
	for _, m := range window {
		if m.Usage == nil {
			continue
		}
		totalActual += int64(m.Usage.ActualTokens())
		if len(pricing) > 0 {
			totalCost += m.Usage.Cost(pricing[0])
		}
	}

	return ContextWindowUsage{
		WindowTokens:       windowTokens,
		MaxWindowSize:      mws,
		UsageRatio:         ratio,
		MessageCount:       len(s.messages),
		Cursor:             s.cursor,
		ActiveMessageCount: len(window),
		TotalActualTokens:  totalActual,
		TotalCost:          totalCost,
	}
}

// SessionDir 返回存储会话数据的文件系统路径。
// 如果未配置持久化存储或存储无法解析会话目录，则返回空字符串。
func (s *Session) SessionDir() string {
	if s.store != nil {
		dir, _ := s.store.ResolveSessionDir(s.id)
		return dir
	}
	return ""
}

// Store 返回底层的 SessionStore 以便在需要时直接访问。
// 大多数用户应优先使用 Session 上的更高级方法。
func (s *Session) Store() SessionStore { return s.store }

// copyMessages 复制消息切片，返回安全的副本。
// 提取出来避免 All() 和 Current() 重复相同的复制逻辑。
func copyMessages(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out
}

// All 返回会话中所有消息（历史和活跃）的副本。
// 返回的切片可以安全修改而不影响会话。
//
// 如果只需要活跃窗口中的消息，请使用 Current()。
//
// 懒加载：首次访问时自动从存储加载消息。
func (s *Session) All() []Message {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyMessages(s.messages)
}

// Current 返回活跃窗口中的消息（从 cursor 到末尾）。
// 这些是在下次推理时将发送到 LLM 的消息。
//
// Cursor 语义：cursor 是当前窗口的起始偏移量，messages 是完整历史。
// 当前窗口 = messages[cursor:]。TryCompact 清空 = cursor = len(messages)。
//
// 此方法实现懒加载：如果消息尚未从持久化存储加载，
// 它将在首次访问时自动加载它们。
//
// 返回的切片是副本，可以安全修改。
func (s *Session) Current() []Message {
	s.ensureLoaded(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cursor >= len(s.messages) {
		return nil
	}
	return copyMessages(s.messages[s.cursor:])
}

// loadMessages 从持久化存储加载消息和 cursor。
// 返回错误表示加载失败，调用方决定是否处理。
func (s *Session) loadMessages(ctx context.Context) error {
	if s.store == nil {
		return nil
	}

	msgs, err := s.store.Get(ctx, s.id)
	if err != nil {
		return fmt.Errorf("加载消息失败: %w", err)
	}

	cursor, _ := s.store.GetCursor(ctx, s.id)

	s.mu.Lock()
	s.messages = msgs
	s.cursor = cursor
	s.mu.Unlock()

	return nil
}

// ensureLoaded 实现会话消息的懒加载。
// 在首次访问时（当 loaded==false），它从持久化存储加载所有消息。
//
// Cursor 语义：cursor 是当前窗口的起始偏移量（从 store.GetCursor 恢复）。
// messages 是完整历史，当前窗口 = messages[cursor:]。
//
// 此方法是线程安全的，并正确处理并发访问：
// - 第一个调用者获取 loadingMu 并执行加载
// - 并发调用者阻塞直到加载完成，然后看到 loaded==true
func (s *Session) ensureLoaded(ctx context.Context) {
	if s.loaded {
		return
	}

	s.loadingMu.Lock()
	defer s.loadingMu.Unlock()

	// 获取锁后双重检查（另一个 goroutine 可能已加载）
	if s.loaded {
		return
	}

	if s.store == nil {
		s.loaded = true
		return
	}

	// 从存储加载消息和游标
	if err := s.loadMessages(ctx); err != nil {
		// 如果加载失败，仍然标记为已加载以避免重试循环。
		s.loaded = true
		return
	}

	// 从会话元数据恢复项目目录以进行文件操作。
	if info, infoErr := s.store.GetMeta(ctx, s.id); infoErr == nil && s.projectDir == "" {
		s.mu.Lock()
		s.projectDir = info.ProjectDir
		s.mu.Unlock()
	}

	// 从存储恢复追踪的已修改文件（如果有）。
	s.loadModifyFiles()

	s.loaded = true
}

// Restore 显式地从持久化存储加载历史消息到内存。
//
// 注意：在懒加载架构中（在 ensureLoaded 中实现），此方法是可选的。
// Current() 和 Append() 会在首次访问时自动加载消息（如果尚未加载）。
//
// 偏移量模型：从 store 恢复 messages（完整历史）和 cursor（偏移量）。
//
// 参数：
//   - ctx: 用于取消和超时控制的上下文
//
// 返回：
//   - error: 如果从存储加载失败
func (s *Session) Restore(ctx context.Context) error {
	if err := s.loadMessages(ctx); err != nil {
		return fmt.Errorf("恢复会话 %s: %w", s.id, err)
	}

	s.loadingMu.Lock()
	s.loaded = true
	s.loadingMu.Unlock()

	return nil
}

// MarkAsContentRef 在活跃窗口中通过 ToolCallID 查找工具消息，
// 将其 Compacted 字段设置为 refTag，然后持久化更改。
//
// 存储优先：先持久化到存储，成功后再更新内存。如果存储失败，返回错误，内存保持不变。
//
// 返回：
//   - error: 如果持久化失败，返回错误。如果未找到消息，返回 nil。
func (s *Session) MarkAsContentRef(toolCallID, refTag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 先查找目标消息
	var targetIdx int = -1
	for i := s.cursor; i < len(s.messages); i++ {
		if s.messages[i].Role == "tool" && s.messages[i].ToolCallID == toolCallID {
			targetIdx = i
			break
		}
	}
	if targetIdx == -1 {
		return nil
	}

	// 存储优先：先持久化
	if s.store != nil {
		// 创建临时副本用于持久化
		tempMessages := make([]Message, len(s.messages))
		copy(tempMessages, s.messages)
		tempMessages[targetIdx].Compacted = refTag

		if err := s.store.UpdateMessages(context.Background(), s.id, s.cursor, tempMessages); err != nil {
			return fmt.Errorf("持久化内容引用标记失败: %w", err)
		}
	}

	// 成功后更新内存
	s.messages[targetIdx].Compacted = refTag
	return nil
}

// Append 向会话添加新消息。
//
// 存储优先：先持久化到存储，成功后再更新内存。如果存储失败，返回错误，内存保持不变。
//
// 偏移量模型：messages 是完整历史，cursor 是当前窗口起始偏移量。
// Append 只追加消息到 messages 末尾，cursor 保持不变（新消息自然落入
// 当前窗口 messages[cursor:]，因为它们在 cursor 之后）。
//
// 摘要触发（TryCompact）不在 Append 末尾调用 —— 改由 runtime 在新一个
// 轮次开始前调用，避免工具结果 append 中途触发清空破坏 tool_call 配对。
//
// 此操作是线程安全的，可以从多个 goroutine 并发调用（例如，工具执行结果流式传入）。
//
// 懒加载：如果这是会话的首次操作，它会在追加前自动从持久化存储加载历史消息。
// 这确保新消息被追加到正确的历史中。
//
// 参数：
//   - ctx: 用于取消和超时控制的上下文
//   - msgs: 要追加的一条或多条消息
//
// 返回：
//   - error: 如果持久化失败，返回第一个遇到的错误。内存保持不变。
//
// 副作用：
//   - 将消息持久化到配置的 SessionStore
func (s *Session) Append(ctx context.Context, msgs ...Message) error {
	s.ensureLoaded(ctx)

	var filtered []Message
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) != "" || len(m.ToolCalls) > 0 {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 存储优先：先持久化
	if s.store != nil {
		for _, msg := range filtered {
			if err := s.store.Append(ctx, s.id, s.agentName, s.sponsor, msg); err != nil {
				return fmt.Errorf("持久化消息失败: %w", err)
			}
		}
	}

	// 成功后更新内存
	s.messages = append(s.messages, filtered...)
	// cursor 不变 —— 它是当前窗口的起始偏移量，Append 只追加到末尾

	return nil
}

// getRoundRange 返回指定游标所在轮次的结束索引（exclusive）。
// active 必须为 messages[s.cursor:]，cursor 为其中的相对偏移量。
// 轮次范围 = [cursor, roundEnd)。
func (s *Session) getRoundRange(cursor int, active []Message) (roundEnd int, err error) {
	if cursor < 0 || cursor >= len(active) {
		return 0, fmt.Errorf("session: cursor %d out of range [0, %d)", cursor, len(active))
	}
	if active[cursor].Role != "user" {
		return 0, fmt.Errorf("session: cursor %d points to %q message, must point to user message",
			cursor, active[cursor].Role)
	}
	roundEnd = cursor + 1
	for roundEnd < len(active) {
		if active[roundEnd].Role == "user" {
			break
		}
		roundEnd++
	}
	return roundEnd, nil
}

// GetRound 获取指定游标所在轮次的所有消息（不含后续轮次）。
//
// 游标 cursor 是当前活跃窗口（messages[cursor:]）内的相对偏移量，0 为起点。
// 游标必须指向一条 User 消息，否则返回 error。
// 返回从该 User 消息起、到下一个 User 消息或窗口末尾为止的完整轮次切片。
func (s *Session) GetRound(ctx context.Context, cursor int) ([]Message, error) {
	s.ensureLoaded(ctx)

	s.mu.RLock()
	defer s.mu.RUnlock()

	if cursor < 0 || cursor >= len(s.messages[s.cursor:]) {
		return nil, fmt.Errorf("session: cursor %d out of range [0, %d)", cursor, len(s.messages[s.cursor:]))
	}
	active := s.messages[s.cursor:]
	roundEnd, err := s.getRoundRange(cursor, active)
	if err != nil {
		return nil, err
	}

	out := make([]Message, roundEnd-cursor)
	copy(out, active[cursor:roundEnd])
	return out, nil
}

// findMessageIndexByTimestamp 在消息切片中查找指定时间戳的消息索引。
// 返回 -1 表示未找到。
func findMessageIndexByTimestamp(msgs []Message, timestamp int64) int {
	for i, m := range msgs {
		if m.Timestamp == timestamp {
			return i
		}
	}
	return -1
}

// DeleteRound 删除指定消息所在的完整轮次。
//
// messageID 是消息的唯一标识（Message.Timestamp），必须在活跃窗口中存在。
// 该方法会找到该消息在活跃窗口（messages[cursor:]）中的位置，
// 确认其为 User 消息后，删除整个轮次。
//
// 存储优先：先从存储删除，成功后再从内存删除。如果存储删除失败，返回错误，内存保持不变。
func (s *Session) DeleteRound(ctx context.Context, messageID int64) error {
	s.ensureLoaded(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()

	active := s.messages[s.cursor:]

	// 按 Timestamp 查找消息在活跃窗口中的位置
	cursor := findMessageIndexByTimestamp(active, messageID)
	if cursor < 0 {
		return fmt.Errorf("session: message %d not found in active window", messageID)
	}

	roundEnd, err := s.getRoundRange(cursor, active)
	if err != nil {
		return err
	}

	absStart := s.cursor + cursor
	absEnd := s.cursor + roundEnd

	// 存储优先：先从存储删除
	if s.store != nil {
		for i := absStart; i < absEnd; i++ {
			if err := s.store.Delete(ctx, s.messages[i].Timestamp, s.id); err != nil {
				return fmt.Errorf("session: 删除消息失败 (index=%d): %w", i, err)
			}
		}
	}

	// 成功后从内存删除
	s.messages = append(s.messages[:absStart], s.messages[absEnd:]...)

	return nil
}

// Reset 清除所有消息并将游标重置为零。
// 这实际上创建了一个空白会话，同时保留相同的 ID 和配置。
//
// 存储优先：先清空存储，成功后再清空内存。如果存储失败，返回错误，内存保持不变。
//
// 使用场景：
//   - 在同一会话中开始新的对话
//   - 从内存中清除敏感数据
//   - 测试和调试
//
// 返回：
//   - error: 如果存储清空失败，返回错误。内存保持不变。
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 存储优先：先清空存储
	if s.store != nil {
		if err := s.store.Clear(context.Background(), s.id); err != nil {
			return fmt.Errorf("清空存储失败: %w", err)
		}
		// 重置游标到存储
		if err := s.store.SetCursor(context.Background(), s.id, 0); err != nil {
			return fmt.Errorf("重置游标失败: %w", err)
		}
	}

	// 成功后清空内存
	s.messages = make([]Message, 0)
	s.cursor = 0

	return nil
}

// Truncate 移除给定索引及之后的所有消息，只保留前 keepCount 条消息。
// 这用于重试场景，在重新发送前需要撤销最后一次交互。
//
// 偏移量模型：截断消息后，若 cursor 超过截断点则回退到截断点，
// 避免 cursor 指向已不存在的消息。cursor 不会前进。
// 如果配置了 SessionStore，更改会被持久化。
func (s *Session) Truncate(ctx context.Context, keepCount int) error {
	s.ensureLoaded(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()

	if keepCount < 0 {
		return fmt.Errorf("keepCount 必须 >= 0,但得到 %d", keepCount)
	}
	if keepCount >= len(s.messages) {
		return nil // 无需截断
	}

	s.messages = s.messages[:keepCount]
	// cursor 不能超过截断点，否则会指向不存在的消息
	if s.cursor > keepCount {
		s.cursor = keepCount
	}

	if s.store != nil {
		if err := s.store.Truncate(ctx, s.id, keepCount); err != nil {
			return fmt.Errorf("存储截断失败: %w", err)
		}
	}

	return nil
}

// SetPendingPermission 记录需要用户授权的工具调用。
// 当实现 PermissionRequired.Grant 的工具返回 granted=false 时，运行时会调用此方法，
// 并在用户响应魔法词时（通过 TakePendingPermission）清除它。
//
// 存储在会话上意味着待处理状态可以在 Ask() 边界之间存活 ——
// 这正是魔法词流程所需的：循环停止，用户输入 "PermissionAllow"，
// 运行时在同一会话中查找待处理的调用，并通过实际运行工具来恢复。
func (s *Session) SetPendingPermission(p PendingPermission) {
	s.pendingMu.Lock()
	s.pendingPermission = &p
	s.pendingMu.Unlock()
}

// TakePendingPermission 原子地读取并清除待处理的调用。
// 当运行时在用户消息中检测到权限魔法词时调用此方法。
// 如果没有待处理的调用，返回 nil（在这种情况下，魔法词被视为常规用户消息）。
func (s *Session) TakePendingPermission() *PendingPermission {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.pendingPermission
	s.pendingPermission = nil
	return p
}

package config

import (
	"sync"
	"time"
)

// RuntimeDirectory 提供了 Agent 运行时元数据的注册和管理功能。
// 它维护了一个线程安全的内存映射，用于跟踪所有已注册 Agent 的
// 运行状态、评分和活动信息。
//
// RuntimeDirectory 支持设置最大容量限制（maxSize），当达到上限时
// 会拒绝新的注册请求。它提供了多种查询方法，包括按状态筛选、
// 按描述搜索以及按评分排序等功能。
//
// 所有公开方法都通过读写锁保证并发安全，适合在多 goroutine 环境中使用。
type RuntimeDirectory struct {
	mu      sync.RWMutex
	agents  map[string]*AgentRuntimeMeta
	maxSize int
}

// NewRuntimeDirectory 创建并返回一个新的 RuntimeDirectory 实例。
//
// 参数 maxSize 指定了目录的最大容量限制：
//  - maxSize > 0: 限制最多注册 maxSize 个 Agent，超出则返回 ErrRuntimeDirFull 错误
//  - maxSize <= 0: 不限制容量，可以注册任意数量的 Agent
//
// 返回的实例已完成初始化，可以直接用于注册和管理 Agent 运行时元数据。
func NewRuntimeDirectory(maxSize int) *RuntimeDirectory {
	if maxSize <= 0 {
		maxSize = 0
	}
	return &RuntimeDirectory{
		agents:  make(map[string]*AgentRuntimeMeta),
		maxSize: maxSize,
	}
}

// Register 将 Agent 运行时元数据注册到目录中。
//
// 如果同名 Agent 已经存在，返回 ErrRuntimeDirDuplicate 错误。
// 如果目录已达到最大容量限制，返回 ErrRuntimeDirFull 错误。
//
// 注册成功后，Agent 可以通过其 ID（即配置名称）进行查询和管理。
func (d *RuntimeDirectory) Register(meta *AgentRuntimeMeta) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.agents[meta.ID()]; exists {
		return ErrRuntimeDirDuplicate
	}
	if d.maxSize > 0 && len(d.agents) >= d.maxSize {
		return ErrRuntimeDirFull
	}
	d.agents[meta.ID()] = meta
	return nil
}

// Unregister 从目录中移除指定 ID 的 Agent。
// 如果 Agent 不存在，该方法静默返回，不产生错误。
func (d *RuntimeDirectory) Unregister(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.agents, id)
}

// Get 根据 ID 查找并返回 Agent 的运行时元数据副本。
// 如果未找到对应 ID 的 Agent，返回 nil。
//
// 返回的是元数据的深拷贝，修改不会影响存储在目录中的原始数据。
func (d *RuntimeDirectory) Get(id string) *AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if meta, ok := d.agents[id]; ok {
		cp := *meta
		return &cp
	}
	return nil
}

// SetState 更新指定 ID 的 Agent 状态，并自动更新最后活跃时间。
// 如果 Agent 不存在，该方法静默返回，不产生错误。
//
// 状态更新会同时记录当前时间作为 LastActive 时间戳，
// 用于追踪 Agent 的活跃程度和判断是否超时。
func (d *RuntimeDirectory) SetState(id string, state AgentState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.State = state
		meta.LastActive = time.Now()
	}
}

// SetScore 更新指定 ID 的 Agent 评分。
// 评分用于排序和选择最优 Agent，值越高表示 Agent 越优秀。
// 如果 Agent 不存在，该方法静默返回，不产生错误。
func (d *RuntimeDirectory) SetScore(id string, score float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.Score = score
	}
}

// ListAll 返回目录中所有 Agent 的运行时元数据列表（副本）。
// 返回的列表是新生成的，修改不会影响内部存储。
//
// 该方法不进行任何过滤或排序，按内部存储顺序返回。
func (d *RuntimeDirectory) ListAll() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*AgentRuntimeMeta, 0, len(d.agents))
	for _, meta := range d.agents {
		cp := *meta
		result = append(result, &cp)
	}
	return result
}

// ListAvailable 返回当前可接受任务的 Agent 列表（按评分降序排列）。
// 可用 Agent 定义为状态为 Idle 或 Dormant 的 Agent。
//
// 返回的列表已经过深拷贝并按 Score 从高到低排序，
// 方便直接选取评分最高的可用 Agent。
func (d *RuntimeDirectory) ListAvailable() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsAvailable() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

// ListActive 返回当前活跃的 Agent 列表（按评分降序排列）。
// 活跃 Agent 定义为状态不是 Error 的 Agent（包括 Idle、Busy、Coordinating、Dormant）。
//
// 返回的列表已经过深拷贝并按 Score 从高到低排序，
// 用于获取所有正常运行的 Agent 并按优先级排序。
func (d *RuntimeDirectory) ListActive() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

// FindByDescription 根据关键词搜索描述匹配的活跃 Agent。
// 搜索执行大小写不敏感的子串匹配，只返回状态为非 Error 的 Agent。
//
// 返回的列表保持内部存储顺序，不进行评分排序。
// 如果没有匹配的 Agent 或 query 为空字符串，返回空列表。
func (d *RuntimeDirectory) FindByDescription(query string) []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() && containsIgnoreCase(meta.Description(), query) {
			cp := *meta
			result = append(result, &cp)
		}
	}
	return result
}

// Count 返回当前目录中注册的 Agent 总数。
func (d *RuntimeDirectory) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.agents)
}

// IncrementTaskCount 增加指定 ID 的 Agent 任务计数器，并更新最后活跃时间。
// 用于追踪 Agent 处理的任务数量，可作为评估 Agent 工作负载和效率的指标。
// 如果 Agent 不存在，该方法静默返回，不产生错误。
func (d *RuntimeDirectory) IncrementTaskCount(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.TaskCount++
		meta.LastActive = time.Now()
	}
}

var (
	ErrRuntimeDirDuplicate = newRuntimeErr("agent already registered")
	ErrRuntimeDirFull      = newRuntimeErr("runtime directory full")
	ErrRuntimeDirNotFound  = newRuntimeErr("agent not found")
)

type runtimeErr struct{ msg string }

func newRuntimeErr(msg string) error { return &runtimeErr{msg} }
func (e *runtimeErr) Error() string  { return "runtime directory: " + e.msg }

// sortByScore 对 Agent 元数据列表按 Score 字段降序排序（使用插入排序）。
// 排序是原地进行的，Score 相对顺序不确定。
func sortByScore(agents []*AgentRuntimeMeta) {
	for i := 1; i < len(agents); i++ {
		key := agents[i]
		j := i - 1
		for ; j >= 0 && agents[j].Score < key.Score; j-- {
			agents[j+1] = agents[j]
		}
		agents[j+1] = key
	}
}

// containsIgnoreCase 执行大小写不敏感的子串匹配检查。
// 如果 s 包含 substr（忽略大小写），返回 true；否则返回 false。
// 空字符串始终返回 false。
func containsIgnoreCase(s, substr string) bool {
	if len(s) == 0 || len(substr) == 0 {
		return false
	}
	sLower := make([]byte, len(s))
	subLower := make([]byte, len(substr))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		sLower[i] = c
	}
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		subLower[i] = c
	}
	for i := 0; i <= len(sLower)-len(subLower); i++ {
		if string(sLower[i:i+len(subLower)]) == string(subLower) {
			return true
		}
	}
	return false
}

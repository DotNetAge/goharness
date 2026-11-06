package tools

import (
	"sync"
	"sync/atomic"
	"time"
)

// ReadFileState 缓存一次读取操作的状态。
//
// 缓存 key 是 (sessionID, filePath, offset, limit) 四元组：按会话隔离，
// 保证命中回执所引用的「此前读取的结果」确实存在于当前会话的历史中
//（跨会话共享键会导致另一会话被引用不存在的上下文）。
//
// 命中条件：mtime 一致即命中——同键请求（同 offset 同 limit）在文件未变化
// 时重读输出必然逐字节相同（动态默认行数在缓存检查前已确定，请求侧与
// 写入侧键一致），无需比较行数或窗口大小。不满足时 → 全量读取并更新缓存，
// 不做增量合并。
//
// 图片不写入 DedupCache（图片去重收益低）。
type ReadFileState struct {
	Limit     int   // 缓存的窗口行数
	LineCount int   // 实际读取行数
	MtimeMs   int64 // 读取时的文件 mtime（毫秒），命中判断的唯一依据
}

// dedupMaxEntries 是 DedupCache 的条目数上限。超限时整表清空：缓存是纯优化，
// 清空后下一次读取走全量路径即可恢复；sync.Map 不支持 LRU，
// 不为缓存优化引入更复杂的淘汰结构。
// 上限依据：按并发会话数 × 单会话读取键数估算（如 10 会话 × 200 键 = 2000），
// 取 4096 留余量，避免多子代理并行读取时逼近上限触发整表清空使去重失效。
const dedupMaxEntries = 4096

// readFileStates 是全局的 DedupCache
var readFileStates sync.Map

// readStateCount 跟踪 DedupCache 当前条目数，用于超限整表清空。
var readStateCount atomic.Int64

// NegativeCacheEntry 记录一个被确认不存在的路径。
type NegativeCacheEntry struct {
	Path      string    // 不存在的路径
	CreatedAt time.Time // 创建时间
}

const negativeCacheTTL = 5 * time.Minute

// negativeCache 是路径不存在的缓存，避免重复 I/O。
var negativeCache sync.Map

// setNegativeCache 记录一个不存在的路径。
// 写入时惰性清扫已过期条目：negativeCache 无其他删除时机，不清扫则条目物理滞留；
// 写入频率低，清扫代价可忽略。
func setNegativeCache(path string) {
	now := time.Now()
	negativeCache.Range(func(key, value any) bool {
		if entry, ok := value.(NegativeCacheEntry); ok && now.Sub(entry.CreatedAt) >= negativeCacheTTL {
			negativeCache.Delete(key)
		}
		return true
	})
	negativeCache.Store(path, NegativeCacheEntry{
		Path:      path,
		CreatedAt: now,
	})
}

// checkNegativeCache 检查路径是否在 NegativeCache 中（且在 TTL 内）。
func checkNegativeCache(path string) bool {
	val, ok := negativeCache.Load(path)
	if !ok {
		return false
	}
	entry, ok := val.(NegativeCacheEntry)
	if !ok {
		return false
	}
	return time.Since(entry.CreatedAt) < negativeCacheTTL
}

// invalidateNegativeCache 在写入成功后清除 NegativeCache。
// 由 Edit/Write 在成功写入后调用。
func invalidateNegativeCache(path string) {
	negativeCache.Delete(path)
}

// dedupCacheKey 生成缓存 key：(sessionID:filePath:offset:limit)。
// sessionID 为空时退化为无会话前缀键（无会话上下文的调用方）。
func dedupCacheKey(sessionID, filePath string, offset, limit int) string {
	return sessionID + "\x00" + filePath + "\x00" + itoa(offset) + "\x00" + itoa(limit)
}

// itoa 是一个简单的整数转字符串函数。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// getReadFileState 获取缓存的读取状态。
func getReadFileState(cacheKey string) (*ReadFileState, bool) {
	val, ok := readFileStates.Load(cacheKey)
	if !ok {
		return nil, false
	}
	state, ok := val.(*ReadFileState)
	return state, ok
}

// setReadFileState 设置缓存的读取状态。
// 同键覆盖（重读后更新状态）不增加条目计数；仅新增键计数，
// 条目数超过 dedupMaxEntries 时整表清空，保证内存有界。
func setReadFileState(cacheKey string, state *ReadFileState) {
	// LoadOrStore 键已存在时不更新值，覆盖路径需显式 Store 最新状态
	if _, loaded := readFileStates.LoadOrStore(cacheKey, state); loaded {
		readFileStates.Store(cacheKey, state)
		return
	}
	if readStateCount.Add(1) > dedupMaxEntries {
		readFileStates.Range(func(key, _ any) bool {
			readFileStates.Delete(key)
			return true
		})
		readStateCount.Store(0)
	}
}

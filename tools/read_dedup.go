package tools

import (
	"sync"
	"sync/atomic"
	"time"
)

// ReadFileState 缓存一次读取操作的状态。
//
// 缓存 key 是 (filePath, offset, limit) 三元组；命中条件：mtime 一致即命中——
// 同键请求在文件未变化时重读输出必然逐字节相同，无需比较行数或窗口大小。
// 不满足时 → 全量读取并更新缓存，不做增量合并。
//
// 注意：若未来启用 DynamicDefaultLines，写入键用的是动态计算的 maxLines
//（buildTextResult 中动态默认行数在缓存写入前生效），而命中检查请求侧的键
// 也随之变化，键语义需重新审视，避免写入与检查键错位。
//
// 图片不写入 DedupCache（图片去重收益低）。
type ReadFileState struct {
	Limit     int   // 缓存的窗口行数
	LineCount int   // 实际读取行数
	MtimeMs   int64 // 读取时的文件 mtime（毫秒），命中判断的唯一依据
}

// dedupMaxEntries 是 DedupCache 的条目数上限。超限时整表清空：缓存是纯优化，
// flush 安全；sync.Map 不支持 LRU，不为缓存优化引入更复杂的淘汰结构。
const dedupMaxEntries = 256

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

// dedupCacheKey 生成缓存 key：(filePath:offset:limit)
func dedupCacheKey(filePath string, offset, limit int) string {
	return filePath + ":" + itoa(offset) + ":" + itoa(limit)
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
// 条目数超过 dedupMaxEntries 时整表清空，保证内存有界。
func setReadFileState(cacheKey string, state *ReadFileState) {
	if readStateCount.Add(1) > dedupMaxEntries {
		readFileStates.Range(func(key, _ any) bool {
			readFileStates.Delete(key)
			return true
		})
		readStateCount.Store(1)
	}
	readFileStates.Store(cacheKey, state)
}

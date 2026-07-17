package tools

import (
	"sync"
	"time"
)

// ReadFileState 缓存一次全量读操作的结果。
//
// 简化设计（见过度设计审计 #6）：只做全量缓存，不做增量逻辑。
// 缓存 key 是 (filePath, offset, limit) 三元组。
// 命中条件：mtime 一致 且 cached.offset == requested.offset 且 cached.limit >= requested.limit
// 不满足时 → 全量读取并更新缓存，不做增量合并。
//
// 图片不写入 DedupCache（图片去重收益低）。
type ReadFileState struct {
	FilePath  string   // 文件绝对路径
	Offset    int      // 起始偏移
	Limit     int      // 缓存行数
	Content   string   // 缓存内容
	LineCount int      // 实际行数
	MtimeMs   int64    // 读取时的文件 mtime（毫秒）
	SizeBytes int64    // 文件大小
}

const (
	dedupMaxLines    = 500  // 每个缓存最多行数
	dedupMaxBytes    = 50 * 1024 // 总缓存容量上限 50KB
)

// readFileStates 是全局的 DedupCache
var readFileStates sync.Map

// NegativeCacheEntry 记录一个被确认不存在的路径。
type NegativeCacheEntry struct {
	Path      string    // 不存在的路径
	CreatedAt time.Time // 创建时间
}

const negativeCacheTTL = 5 * time.Minute

// negativeCache 是路径不存在的缓存，避免重复 I/O。
var negativeCache sync.Map

// setNegativeCache 记录一个不存在的路径。
func setNegativeCache(path string) {
	negativeCache.Store(path, NegativeCacheEntry{
		Path:      path,
		CreatedAt: time.Now(),
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
func setReadFileState(cacheKey string, state *ReadFileState) {
	readFileStates.Store(cacheKey, state)
}

// clearReadFileState 删除缓存的读取状态。
// 用于清理操作，以节省内存。
func clearReadFileState(cacheKey string) {
	readFileStates.Delete(cacheKey)
}

package tools

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DotNetAge/goharness/logging"
)

// DefaultToolRegistry 是 ToolRegistry 的默认实现。
// 它提供线程安全的工具注册、查找和过滤能力。
//
// 该注册表维护一个以工具名称为键的工具 map，并支持：
//   - 工具的注册和移除
//   - 按名称查找
//   - 基于关键词、安全级别、允许名称或搜索词的过滤
//   - 通过 sync.RWMutex 实现线程安全的并发访问
type DefaultToolRegistry struct {
	mu     sync.RWMutex
	tools  map[string]FuncTool
	logger logging.Logger
}

var _ ToolRegistry = (*DefaultToolRegistry)(nil)

// NewDefaultToolRegistry 创建一个带默认 logger 的空 DefaultToolRegistry。
// 返回的注册表可直接用于工具注册和并发使用。
func NewDefaultToolRegistry() *DefaultToolRegistry {
	return &DefaultToolRegistry{
		tools:  make(map[string]FuncTool),
		logger: logging.DefaultLogger(),
	}
}

// Register 将一个工具添加到注册表中。
// 若已有同名工具被注册，则返回错误。
// 工具的 Info().Name 用作注册表的键。
func (r *DefaultToolRegistry) Register(tool FuncTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Info().Name
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("工具 %q 已注册", name)
	}
	r.tools[name] = tool
	return nil
}

// Remove 按名称从注册表中删除工具。
// 若未找到指定名称的工具，则返回错误。
func (r *DefaultToolRegistry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return fmt.Errorf("未找到工具 %q", name)
	}
	delete(r.tools, name)
	return nil
}

// Get 按名称获取工具。
// 找到则返回该工具和 true，未找到则返回零值和 false。
func (r *DefaultToolRegistry) Get(name string) (FuncTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All 返回所有已注册工具的切片。
// 工具的顺序不确定（map 迭代顺序）。
func (r *DefaultToolRegistry) All() []FuncTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FuncTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// FindAvailable 返回匹配给定过滤条件的工具。
//
// 过滤逻辑（按优先级顺序）：
//   - 若 filter 为 nil 或空，则返回所有工具
//   - 若指定了 AllowedNames，则仅返回该列表中的工具（精确名称匹配）
//   - 否则，按关键词（与标签、名称、描述匹配）、
//     安全级别和搜索词（在描述和标签中搜索）进行过滤
//
// 若关键词/搜索词过滤未匹配到任何工具，则记录一条警告并返回所有工具。
func (r *DefaultToolRegistry) FindAvailable(filter *ToolFilter) []FuncTool {
	if filter == nil || (len(filter.Keywords) == 0 && len(filter.AllowedNames) == 0 && filter.Security == 0 && filter.Terms == "") {
		return r.All()
	}

	allTools := r.All()

	if len(filter.AllowedNames) > 0 {
		allowedSet := make(map[string]bool, len(filter.AllowedNames))
		for _, n := range filter.AllowedNames {
			allowedSet[n] = true
		}
		var filtered []FuncTool
		for _, t := range allTools {
			if allowedSet[t.Info().Name] {
				filtered = append(filtered, t)
			}
		}
		return filtered
	}

	lowerKeywords := make(map[string]bool, len(filter.Keywords))
	for _, kw := range filter.Keywords {
		lowerKeywords[strings.ToLower(strings.TrimSpace(kw))] = true
	}

	var matched []FuncTool
	for _, t := range allTools {
		info := t.Info()
		if r.toolMatchesFilter(info, filter, lowerKeywords) {
			matched = append(matched, t)
		}
	}

	if len(matched) == 0 {
		r.logger.Warn("tool registry: filter matched no tools, returning all tools",
			"tool_count", len(allTools),
		)
		return allTools
	}
	return matched
}

// toolMatchesFilter 检查工具的 info 是否匹配过滤条件。
// 所有条件以 AND 方式组合：工具必须通过所有非零的过滤字段。
//
// 匹配规则：
//   - Security：若指定则需精确匹配
//   - Keywords：与 tags 不区分大小写匹配（任一命中即通过），
//     再与 name 和 description 进行子串匹配
//   - Terms：在 description 和 tags 中不区分大小写搜索
func (r *DefaultToolRegistry) toolMatchesFilter(info *ToolInfo, filter *ToolFilter, keywords map[string]bool) bool {

	if filter.Security != 0 && info.SecurityLevel != filter.Security {
		return false
	}

	if len(keywords) > 0 {
		for _, tag := range info.Tags {
			if keywords[strings.ToLower(tag)] {
				return true
			}
		}
		nameLower := strings.ToLower(info.Name)
		descLower := strings.ToLower(info.Description)
		for kw := range keywords {
			if strings.Contains(nameLower, kw) || strings.Contains(descLower, kw) {
				return true
			}
		}
		return false
	}

	if filter.Terms != "" {
		termsLower := strings.ToLower(filter.Terms)
		descLower := strings.ToLower(info.Description)
		if strings.Contains(descLower, termsLower) {
			return true
		}
		for _, tag := range info.Tags {
			if strings.Contains(strings.ToLower(tag), termsLower) {
				return true
			}
		}
		return false
	}

	return true
}

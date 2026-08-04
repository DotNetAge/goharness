package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionWhitelist 定义了会话级别的工具白名单。
// 每个字段对应一个工具类型，存储允许执行的命令或路径列表。
type SessionWhitelist struct {
	Bash      []string `json:"bash,omitempty"`
	Write     []string `json:"write,omitempty"`
	Edit      []string `json:"edit,omitempty"`
	RunScript []string `json:"run_script,omitempty"`
	Read      []string `json:"read,omitempty"`
	Ls        []string `json:"ls,omitempty"`
}

// whitelistFileName 是存储在每个会话目录中的 JSON 文件名。
const whitelistFileName = "session-wl.json"

// whitelistPath 返回会话白名单文件的绝对路径。
// 当会话没有持久化目录（无 store）时返回空字符串。
func (s *Session) whitelistPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, whitelistFileName)
}

// Whitelist 返回会话白名单，从磁盘懒加载。
// 结果会缓存在内存中，因此同一会话内重复访问不会重复读取文件。
// 当文件不存在或会话没有持久化目录时，返回空（非 nil）白名单。
func (s *Session) Whitelist() *SessionWhitelist {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	if s.whitelist != nil {
		return s.whitelist
	}

	// 从磁盘懒加载。
	s.whitelist = &SessionWhitelist{}
	wp := s.whitelistPath()
	if wp == "" {
		return s.whitelist
	}

	data, err := os.ReadFile(wp)
	if err != nil {
		// 文件尚不存在 —— 返回空白名单。
		return s.whitelist
	}

	// 尽力解析；格式错误的文件会重置为空白名单。
	var wl SessionWhitelist
	if json.Unmarshal(data, &wl) == nil {
		s.whitelist = &wl
	}
	return s.whitelist
}

// AddToWhitelist 为指定工具添加一条会话白名单条目，
// 并将更新后的白名单持久化到 {SessionDir()}/session-wl.json。
//
// 参数：
//   - toolName: "bash"、"write"、"edit"、"run_script"、"read"、"ls" 之一
//   - entry:    要添加的值（bash 为基础命令名；其他为文件/脚本路径）
//
// 当工具名未知或持久化失败时返回错误。
// 重复条目会被静默忽略。
func (s *Session) AddToWhitelist(toolName, entry string) error {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	// 确保白名单已加载。
	if s.whitelist == nil {
		s.whitelist = &SessionWhitelist{}
	}

	// 选择该工具名对应的切片。
	var target *[]string
	switch strings.ToLower(toolName) {
	case "bash":
		target = &s.whitelist.Bash
	case "write":
		target = &s.whitelist.Write
	case "edit":
		target = &s.whitelist.Edit
	case "run_script":
		target = &s.whitelist.RunScript
	case "read":
		target = &s.whitelist.Read
	case "ls":
		target = &s.whitelist.Ls
	default:
		return fmt.Errorf("会话白名单中存在未知的工具 %q", toolName)
	}

	// 跳过重复项。
	for _, existing := range *target {
		if existing == entry {
			return nil
		}
	}

	*target = append(*target, entry)

	// 持久化到磁盘。
	wp := s.whitelistPath()
	if wp == "" {
		// 无持久化目录 —— 仅内存保存即可。
		return nil
	}

	data, err := json.MarshalIndent(s.whitelist, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话白名单失败: %w", err)
	}

	if err := os.WriteFile(wp, data, 0644); err != nil {
		return fmt.Errorf("写入会话白名单 %s 失败: %w", wp, err)
	}

	return nil
}

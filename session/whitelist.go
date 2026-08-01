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

// whitelistFileName is the JSON file name stored in each session directory.
const whitelistFileName = "session-wl.json"

// whitelistPath returns the absolute path to the session whitelist file.
// Returns "" when the session has no persistent directory (no store).
func (s *Session) whitelistPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, whitelistFileName)
}

// Whitelist returns the session whitelist, lazily loaded from disk.
// The result is cached in memory so repeated access within a session
// does not re-read the file. Returns an empty (non-nil) whitelist when
// no file exists or the session has no persistent directory.
func (s *Session) Whitelist() *SessionWhitelist {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	if s.whitelist != nil {
		return s.whitelist
	}

	// Lazy load from disk.
	s.whitelist = &SessionWhitelist{}
	wp := s.whitelistPath()
	if wp == "" {
		return s.whitelist
	}

	data, err := os.ReadFile(wp)
	if err != nil {
		// File doesn't exist yet — return empty whitelist.
		return s.whitelist
	}

	// Best-effort parse; malformed file resets to empty whitelist.
	var wl SessionWhitelist
	if json.Unmarshal(data, &wl) == nil {
		s.whitelist = &wl
	}
	return s.whitelist
}

// AddToWhitelist adds an entry to the session whitelist for the given tool
// and persists the updated whitelist to {SessionDir()}/session-wl.json.
//
// Parameters:
//   - toolName: one of "bash", "write", "edit", "run_script", "read", "ls"
//   - entry:    the value to add (base command name for bash; file/script path for others)
//
// Returns an error if the tool name is unrecognised, or if persistence fails.
// Duplicate entries are silently ignored.
func (s *Session) AddToWhitelist(toolName, entry string) error {
	s.whitelistMu.Lock()
	defer s.whitelistMu.Unlock()

	// Ensure whitelist is loaded.
	if s.whitelist == nil {
		s.whitelist = &SessionWhitelist{}
	}

	// Select the slice for this tool name.
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

	// Skip duplicates.
	for _, existing := range *target {
		if existing == entry {
			return nil
		}
	}

	*target = append(*target, entry)

	// Persist to disk.
	wp := s.whitelistPath()
	if wp == "" {
		// No persistent directory — in-memory only is fine.
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

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试条目取自真实消费语义（agents/permission.go buildWhitelistEntry）：
//   - bash 存基础命令名（"git push origin main" → "git"）
//   - write/edit/read/ls 存解析后的绝对路径
//   - network 存 URL host（授权的是"访问该主机"这一事实）
//   - 传入的 toolName 是工具真实名称（如 "Bash"），AddToWhitelist 内部转小写

// TestSession_Whitelist_AddAndPersist 验证授权后白名单写入、落盘与跨 Load 恢复：
// daemon 每轮 user.message 重新 Load 全新 Session 实例，白名单必须从
// session-wl.json 懒加载恢复，否则用户「本会话允许」的授权形同虚设。
func TestSession_Whitelist_AddAndPersist(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	s1 := newTestSession("sess-wl", "agent", store)

	// 模拟 PermissionAllowSession 流程：Bash 授权（基础命令名）+ 网段授权（host）
	if err := s1.AddToWhitelist("Bash", "git"); err != nil {
		t.Fatalf("AddToWhitelist(bash) 失败: %v", err)
	}
	if err := s1.AddToWhitelist("network", "api.github.com"); err != nil {
		t.Fatalf("AddToWhitelist(network) 失败: %v", err)
	}
	// 文件路径授权（ResolveTargetPath 解析后的绝对路径形态）
	path := filepath.Join(dir, "src", "main.go")
	if err := s1.AddToWhitelist("write", path); err != nil {
		t.Fatalf("AddToWhitelist(write) 失败: %v", err)
	}

	wl := s1.Whitelist()
	if len(wl.Bash) != 1 || wl.Bash[0] != "git" {
		t.Errorf("内存白名单 Bash = %v，期望 [git]", wl.Bash)
	}
	if len(wl.Network) != 1 || wl.Network[0] != "api.github.com" {
		t.Errorf("内存白名单 Network = %v，期望 [api.github.com]", wl.Network)
	}
	if len(wl.Write) != 1 || wl.Write[0] != path {
		t.Errorf("内存白名单 Write = %v，期望 [%s]", wl.Write, path)
	}

	// 验证落盘文件名与 JSON 键
	wp := filepath.Join(dir, "session-wl.json")
	data, err := os.ReadFile(wp)
	if err != nil {
		t.Fatalf("session-wl.json 未写入: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("落盘内容不是合法 JSON: %v", err)
	}
	if _, ok := raw["bash"]; !ok {
		t.Errorf("落盘 JSON 缺少 bash 键: %s", data)
	}
	if _, ok := raw["network"]; !ok {
		t.Errorf("落盘 JSON 缺少 network 键: %s", data)
	}

	// 重建 Session（等价 daemon 下一轮 Load），懒加载应恢复全部条目
	s2 := newTestSession("sess-wl", "agent", store)
	wl2 := s2.Whitelist()
	if len(wl2.Bash) != 1 || wl2.Bash[0] != "git" {
		t.Errorf("重建后 Bash = %v，期望 [git]", wl2.Bash)
	}
	if len(wl2.Network) != 1 || wl2.Network[0] != "api.github.com" {
		t.Errorf("重建后 Network = %v，期望 [api.github.com]", wl2.Network)
	}
	if len(wl2.Write) != 1 || wl2.Write[0] != path {
		t.Errorf("重建后 Write = %v，期望 [%s]", wl2.Write, path)
	}
}

// TestSession_Whitelist_AllToolNames 验证 7 个工具名各自路由到对应切片。
func TestSession_Whitelist_AllToolNames(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir
	s := newTestSession("sess-wl-all", "agent", store)

	entries := map[string]string{
		"bash":       "docker",
		"write":      "/tmp/out.txt",
		"edit":       "/tmp/app.conf",
		"run_script": "/tmp/deploy.sh",
		"read":       "/tmp/data.csv",
		"ls":         "/var/log",
		"network":    "registry.npmjs.org",
	}
	for tool, entry := range entries {
		if err := s.AddToWhitelist(tool, entry); err != nil {
			t.Fatalf("AddToWhitelist(%s) 失败: %v", tool, err)
		}
	}

	wl := s.Whitelist()
	if len(wl.Bash) != 1 || wl.Bash[0] != "docker" {
		t.Errorf("Bash = %v", wl.Bash)
	}
	if len(wl.Write) != 1 || wl.Write[0] != "/tmp/out.txt" {
		t.Errorf("Write = %v", wl.Write)
	}
	if len(wl.Edit) != 1 || wl.Edit[0] != "/tmp/app.conf" {
		t.Errorf("Edit = %v", wl.Edit)
	}
	if len(wl.RunScript) != 1 || wl.RunScript[0] != "/tmp/deploy.sh" {
		t.Errorf("RunScript = %v", wl.RunScript)
	}
	if len(wl.Read) != 1 || wl.Read[0] != "/tmp/data.csv" {
		t.Errorf("Read = %v", wl.Read)
	}
	if len(wl.Ls) != 1 || wl.Ls[0] != "/var/log" {
		t.Errorf("Ls = %v", wl.Ls)
	}
	if len(wl.Network) != 1 || wl.Network[0] != "registry.npmjs.org" {
		t.Errorf("Network = %v", wl.Network)
	}
}

// TestSession_Whitelist_DuplicateIgnored 验证重复条目静默忽略，
// 且跨 Session 实例（磁盘懒加载后）同样去重。
func TestSession_Whitelist_DuplicateIgnored(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir

	s1 := newTestSession("sess-wl-dup", "agent", store)
	if err := s1.AddToWhitelist("bash", "git"); err != nil {
		t.Fatalf("首次添加失败: %v", err)
	}
	if err := s1.AddToWhitelist("bash", "git"); err != nil {
		t.Fatalf("同实例重复添加失败: %v", err)
	}
	if got := len(s1.Whitelist().Bash); got != 1 {
		t.Fatalf("同实例重复条目未去重: Bash = %d 条", got)
	}

	// 重建实例后从磁盘加载，再添加同条目仍应去重
	s2 := newTestSession("sess-wl-dup", "agent", store)
	if err := s2.AddToWhitelist("bash", "git"); err != nil {
		t.Fatalf("跨实例重复添加失败: %v", err)
	}
	if got := len(s2.Whitelist().Bash); got != 1 {
		t.Errorf("跨实例重复条目未去重: Bash = %d 条", got)
	}
}

// TestSession_Whitelist_UnknownTool 验证未知工具名返回错误且不落盘。
func TestSession_Whitelist_UnknownTool(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir
	s := newTestSession("sess-wl-unknown", "agent", store)

	err := s.AddToWhitelist("web_search", "https://example.com")
	if err == nil {
		t.Fatal("未知工具名应返回错误")
	}
	if !strings.Contains(err.Error(), "web_search") {
		t.Errorf("错误信息应包含工具名，实际: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "session-wl.json")); !os.IsNotExist(statErr) {
		t.Error("未知工具名不应产生落盘文件")
	}
}

// TestSession_Whitelist_NoSessionDir 验证无持久化目录的会话（纯内存）
// 仅内存保存，不落盘也不报错。
func TestSession_Whitelist_NoSessionDir(t *testing.T) {
	s := newTestSession("sess-wl-mem", "agent", newMockStore())

	if err := s.AddToWhitelist("bash", "rg"); err != nil {
		t.Fatalf("无持久化目录时不应报错: %v", err)
	}
	if got := len(s.Whitelist().Bash); got != 1 {
		t.Errorf("内存白名单应有 1 条，实际 %d", got)
	}
	// Whitelist() 应返回非 nil 空白名单而非 panic
	empty := newTestSession("sess-wl-empty", "agent", newMockStore()).Whitelist()
	if empty == nil {
		t.Fatal("无文件时 Whitelist() 应返回非 nil 空白名单")
	}
}

// TestSession_Whitelist_CorruptFileReset 验证损坏的 session-wl.json
// 被重置为空白名单，且后续 Add 能恢复落盘（半截 JSON 不得永久阻断授权）。
func TestSession_Whitelist_CorruptFileReset(t *testing.T) {
	dir := t.TempDir()
	store := newMockStore()
	store.sessionDir = dir
	wp := filepath.Join(dir, "session-wl.json")

	// 模拟半截写入
	if err := os.WriteFile(wp, []byte(`{"bash": ["gi`), 0644); err != nil {
		t.Fatalf("写损坏文件失败: %v", err)
	}

	s := newTestSession("sess-wl-corrupt", "agent", store)
	wl := s.Whitelist()
	if wl == nil || len(wl.Bash) != 0 {
		t.Errorf("损坏文件应重置为空白名单，实际 Bash = %v", wl.Bash)
	}

	// 损坏后仍可继续添加并恢复合法落盘
	if err := s.AddToWhitelist("bash", "git"); err != nil {
		t.Fatalf("损坏后添加失败: %v", err)
	}
	s2 := newTestSession("sess-wl-corrupt", "agent", store)
	if got := s2.Whitelist().Bash; len(got) != 1 || got[0] != "git" {
		t.Errorf("恢复落盘后重建应读到 [git]，实际 %v", got)
	}
}

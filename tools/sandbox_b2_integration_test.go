package tools

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 B2 阶段补的集成测试，验证 Bash / WebFetch / RunScript 沙箱接入后的端到端行为。
//
// 覆盖范围：
//  1. Bash 沙箱启用 - 危险命令 Grant 拒绝（rm -rf /）
//  2. Bash 沙箱启用 - 白名单外命令 Grant 触发授权
//  3. Bash 沙箱启用 - 白名单内命令 Grant 放行
//  4. Bash 沙箱启用 - 会话级白名单命中 Grant 放行
//  5. Bash 沙箱启用 - Execute 阶段拦截危险命令（Grant 被绕过兜底）
//  6. Bash 沙箱启用 - 网络命令 URL 预检拒绝 SSRF
//  7. Bash 沙箱未注入 - 拒绝执行（安全决策统一收口到沙箱）
//  8. WebFetch 沙箱启用 - SSRF URL 拒绝
//  9. WebFetch 沙箱未启用 - 回退旧逻辑
// 10. RunScript 沙箱启用 - Execute EnforceFile 拦截敏感脚本（TOCTOU 防护）

// newSandboxCtxB2 复用 B1 的辅助函数（在同包内）。
// 为避免命名冲突，这里直接复用 newSandboxCtx。

// newBashSandboxCtx 构造带沙箱的 Bash 测试上下文。
// networkAllow 用于放行特定内网 IP（测试 SSRF 放行场景）。
func newBashSandboxCtx(t *testing.T, projectDir string, extraModify func(*sandbox.SandboxPolicy)) context.Context {
	t.Helper()
	policy := sandbox.SandboxPolicy{
		AllowedDirs:           []string{projectDir},
		DeniedFileGlobs:       sandbox.DefaultDeniedFileGlobs(),
		DeniedDirGlobs:        sandbox.DefaultDeniedDirGlobs(),
		DeniedDevicePaths:     sandbox.DefaultDeniedDevicePaths(),
		NetworkDenySubnets:    sandbox.DefaultDeniedSubnets(),
		AllowedCommands:       sandbox.DefaultAllowedCommands(),
		DeniedCommandPatterns: sandbox.DefaultDeniedCommandPatterns(),
		NetworkCommands:       sandbox.DefaultNetworkCommands(),
	}
	if extraModify != nil {
		extraModify(&policy)
	}
	sb, err := sandbox.NewSandbox(&policy, logging.NewNopLogger())
	require.NoError(t, err)

	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger(), session.WithSandbox(sb))
	require.NoError(t, err)

	return WithToolContext(context.Background(), &ToolContext{
		Session:          sess,
		SessionWhitelist: sess.Whitelist(),
		Logger:           logging.NewNopLogger(),
	})
}

// ----- Bash 工具沙箱集成测试 -----

// TestBash_Sandbox_DangerousCommand_Denies 验证沙箱启用时危险命令 Grant 拒绝。
func TestBash_Sandbox_DangerousCommand_Denies(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	granted, reason := bash.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "rm -rf /",
	})
	assert.False(t, granted, "沙箱启用时危险命令 rm -rf / 应被拒绝")
	assert.NotEmpty(t, reason)
}

// TestBash_Sandbox_WhitelistedCommand_Allows 验证沙箱启用时白名单内命令 Grant 放行。
func TestBash_Sandbox_WhitelistedCommand_Allows(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	granted, _ := bash.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "ls -la " + projectDir,
	})
	assert.True(t, granted, "沙箱启用时白名单内命令 ls 应放行")
}

// TestBash_Sandbox_NonWhitelistedCommand_AsksUser 验证沙箱启用时非白名单命令 Grant 触发授权。
// 注：沙箱 AllowedCommands 默认列表很全，用一个不存在的命令测试。
func TestBash_Sandbox_NonWhitelistedCommand_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	granted, _ := bash.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "nonexistent_cmd_xyz --flag",
	})
	assert.False(t, granted, "沙箱启用时非白名单命令应触发授权")
}

// TestBash_Sandbox_SessionWhitelist_Allows 验证沙箱启用时会话级白名单命中 Grant 放行。
func TestBash_Sandbox_SessionWhitelist_Allows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newBashSandboxCtx(t, projectDir, nil)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("bash", "nonexistent_cmd_xyz"))

	bash := NewBashTool().(*BashTool)
	granted, _ := bash.Grant(ctx, map[string]any{
		"command": "nonexistent_cmd_xyz --flag",
	})
	assert.True(t, granted, "沙箱启用时会话级白名单命中应放行")
}

// TestBash_Sandbox_Execute_BlocksDangerousCommand 验证 Execute 阶段拦截危险命令。
// 模拟 Grant 被绕过（如 PermissionAllow），Execute 仍能拦截。
func TestBash_Sandbox_Execute_BlocksDangerousCommand(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	result, err := bash.Execute(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "rm -rf /",
	})
	require.NoError(t, err, "Execute 返回阻塞结果而非 error")
	m := result.(map[string]any)
	assert.Equal(t, false, m["success"])
	assert.Equal(t, 126, m["exit_code"])
	assert.NotEmpty(t, m["error"])
}

// TestBash_Sandbox_Execute_NormalCommand_Runs 验证沙箱启用时正常命令能执行。
func TestBash_Sandbox_Execute_NormalCommand_Runs(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "test.txt"), []byte("hi"), 0644))
	bash := NewBashTool().(*BashTool)

	result, err := bash.Execute(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "cat " + filepath.Join(projectDir, "test.txt"),
	})
	require.NoError(t, err)
	m := result.(map[string]any)
	assert.Equal(t, true, m["success"])
	assert.Contains(t, m["stdout"].(string), "hi")
}

// TestBash_Sandbox_NetworkCommand_SSRF_Denies 验证网络命令 URL 预检拦截内网地址。
// curl http://192.168.1.1 命中禁止网段 → AskUser → Grant 拒绝（触发授权弹窗）。
// 回环地址已默认放行（访问本机是正常行为），不再作为 SSRF 拦截用例。
func TestBash_Sandbox_NetworkCommand_SSRF_Denies(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	granted, reason := bash.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "curl http://192.168.1.1/",
	})
	assert.False(t, granted, "沙箱启用时 curl 内网 IP 应触发授权（AskUser）")
	assert.Contains(t, reason, "网段")
}

// TestBash_Sandbox_NetworkCommand_SessionNetworkWhitelist_Allows 验证 URL 网段授权闭环：
// 用户 AllowSession 后（host 入会话网络白名单），Grant 与 Execute 两阶段均放行——
// 未授权时 Grant 拒绝弹窗，授权后同会话再访问同一 host 不再弹窗且能真正执行。
func TestBash_Sandbox_NetworkCommand_SessionNetworkWhitelist_Allows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newBashSandboxCtx(t, projectDir, nil)
	tc := GetToolContext(ctx)
	bash := NewBashTool().(*BashTool)

	// 未授权：Grant 拒绝（触发授权弹窗）
	granted, _ := bash.Grant(ctx, map[string]any{"command": "curl http://192.168.1.1/"})
	assert.False(t, granted, "未授权时 Grant 应拒绝")

	// 模拟用户 AllowSession：目标 host 入会话网络白名单
	require.NoError(t, tc.Session.AddToWhitelist("network", "192.168.1.1"))

	// 授权后：Grant 放行
	granted, reason := bash.Grant(ctx, map[string]any{"command": "curl http://192.168.1.1/"})
	assert.True(t, granted, "授权后 Grant 应放行，实际原因=%s", reason)

	// 授权后：Execute 真正执行（192.168.1.1 不可达时错误应为网络错误而非 126 拦截）
	result, err := bash.Execute(ctx, map[string]any{
		"command": "curl -s -m 3 http://192.168.1.1/",
		"timeout": float64(10000),
	})
	require.NoError(t, err)
	m := result.(map[string]any)
	assert.False(t, m["exit_code"] == 126, "授权后 Execute 不应被沙箱拦截，实际结果=%v", m)
}

// TestBash_Sandbox_NetworkCommand_PublicURL_Allows 验证网络命令公网 URL 放行。
// 用一个公网域名测试（不做实际请求，仅 Grant 阶段预检）。
func TestBash_Sandbox_NetworkCommand_PublicURL_Allows(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	// example.com 是 IANA 保留的示例域名，解析到公网 IP
	granted, _ := bash.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "curl -I https://example.com/",
	})
	assert.True(t, granted, "沙箱启用时 curl 公网 URL 应放行")
}

// TestBash_Sandbox_Disabled_RefusesExecution 验证沙箱未注入时拒绝执行。
// 安全决策统一收口到沙箱：Grant 放行（授权无意义），Execute 拒绝执行。
func TestBash_Sandbox_Disabled_RefusesExecution(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newGrantCtx(t, projectDir) // 不注入沙箱
	bash := NewBashTool().(*BashTool)

	// Grant 放行（授权无意义，Execute 阶段拒绝）
	granted, _ := bash.Grant(ctx, map[string]any{"command": "ls"})
	assert.True(t, granted, "沙箱未注入时 Grant 放行（授权无意义）")

	// Execute 拒绝执行（以阻塞结果返回，exit_code=126）
	result, err := bash.Execute(ctx, map[string]any{"command": "ls"})
	require.NoError(t, err, "拒绝以阻塞结果返回而非 error")
	m := result.(map[string]any)
	assert.Equal(t, false, m["success"])
	assert.Contains(t, m["error"].(string), "未注入沙箱")
}

// TestBash_Sandbox_NetworkAllowSubnets_OverridesDeny 验证 NetworkAllowSubnets 放行特定内网。
func TestBash_Sandbox_NetworkAllowSubnets_OverridesDeny(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)

	// 放行 10.0.0.0/8（覆盖默认禁止列表）
	ctx := newBashSandboxCtx(t, projectDir, func(p *sandbox.SandboxPolicy) {
		p.NetworkAllowSubnets = mustParseCIDRList(t, []string{"10.0.0.0/8"})
	})

	granted, _ := bash.Grant(ctx, map[string]any{
		"command": "curl http://10.0.0.1/",
	})
	assert.True(t, granted, "NetworkAllowSubnets 放行 10.0.0.0/8 后应允许访问")
}

// ----- WebFetch 工具沙箱集成测试 -----

// TestWebFetch_Sandbox_SSRF_Denies 验证沙箱启用时内网 URL 被拒绝（AskUser 未授权）。
// WebFetch 实现 PermissionRequired：Grant 阶段拒绝触发授权弹窗；Execute 阶段
// 未授权时同样拒绝。回环地址已默认放行，不再作为拦截用例。
func TestWebFetch_Sandbox_SSRF_Denies(t *testing.T) {
	projectDir := t.TempDir()
	wf := NewWebFetchTool(logging.NewNopLogger()).(*WebFetchTool)

	// Grant 阶段：未授权 host 触发授权
	granted, reason := wf.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"url": "http://192.168.1.1/",
	})
	assert.False(t, granted, "未授权的内网 URL 应触发授权")
	assert.Contains(t, reason, "网段")

	// Execute 阶段：拒绝执行
	_, err := wf.Execute(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"url": "http://192.168.1.1/",
	})
	require.Error(t, err, "未授权的内网 URL 应被拒绝执行")
	assert.Contains(t, err.Error(), "网段")
}

// TestWebFetch_Sandbox_Loopback_Allows 验证回环 URL 默认放行（不触发授权）。
// 本地开发服务器/本地模型服务是 WebFetch 的正常访问目标。
func TestWebFetch_Sandbox_Loopback_Allows(t *testing.T) {
	projectDir := t.TempDir()
	wf := NewWebFetchTool(logging.NewNopLogger()).(*WebFetchTool)

	granted, _ := wf.Grant(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"url": "http://localhost:3000/",
	})
	assert.True(t, granted, "回环 URL 应默认放行（不弹授权窗）")
}

// TestWebFetch_Sandbox_SessionNetworkWhitelist_Allows 验证 WebFetch 的 URL 网段授权闭环：
// host 入会话网络白名单后 Grant 放行；Execute 真正发请求（连不上是网络错误而非 SSRF 拒绝）。
func TestWebFetch_Sandbox_SessionNetworkWhitelist_Allows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newBashSandboxCtx(t, projectDir, nil)
	tc := GetToolContext(ctx)
	wf := NewWebFetchTool(logging.NewNopLogger()).(*WebFetchTool)

	// 未授权：Grant 拒绝
	granted, _ := wf.Grant(ctx, map[string]any{"url": "http://192.168.1.1/"})
	assert.False(t, granted, "未授权时 Grant 应拒绝")

	// 模拟用户 AllowSession
	require.NoError(t, tc.Session.AddToWhitelist("network", "192.168.1.1"))

	// 授权后：Grant 放行
	granted, reason := wf.Grant(ctx, map[string]any{"url": "http://192.168.1.1/"})
	assert.True(t, granted, "授权后 Grant 应放行，实际原因=%s", reason)

	// 授权后：Execute 真正发请求（错误应为网络错误而非 SSRF 网段拒绝）
	_, err := wf.Execute(ctx, map[string]any{"url": "http://192.168.1.1/"})
	if err != nil {
		assert.NotContains(t, err.Error(), "网段", "授权后 Execute 不应再被网段预检拒绝")
	}
}

// TestWebFetch_Sandbox_Disabled_Reject 验证沙箱未注入时 WebFetch 拒绝执行。
// 安全决策统一收口到沙箱：未注入沙箱不再回退到工具内旧 SSRF 逻辑，
// 而是直接拒绝执行（调用方配置错误，授权无法解除）。
func TestWebFetch_Sandbox_Disabled_Reject(t *testing.T) {
	projectDir := t.TempDir()
	wf := NewWebFetchTool(logging.NewNopLogger()).(*WebFetchTool)

	_, err := wf.Execute(newGrantCtx(t, projectDir), map[string]any{
		"url": "http://127.0.0.1/",
	})
	require.Error(t, err, "沙箱未注入时应拒绝执行")
	assert.Contains(t, err.Error(), "未注入沙箱")
}

// ----- RunScript 工具 Execute EnforceFile 测试 -----

// TestRunScript_Sandbox_Execute_EnforceSensitiveScript 验证 Execute 阶段拦截敏感目录脚本。
// 这是 B1 遗漏的 TOCTOU 防护：Grant 阶段检查通过后，Execute 仍做 EnforceFile。
func TestRunScript_Sandbox_Execute_EnforceSensitiveScript(t *testing.T) {
	projectDir := t.TempDir()
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))
	scriptInSensitive := filepath.Join(sshDir, "helper.py")
	require.NoError(t, os.WriteFile(scriptInSensitive, []byte("print('hi')"), 0644))

	rs := NewRunScriptTool()
	_, err := rs.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "python " + scriptInSensitive,
	})
	require.Error(t, err, "沙箱启用时 Execute 应拒绝敏感目录脚本（EnforceFile）")
}

// TestRunScript_Sandbox_Execute_NormalScript_Runs 验证沙箱启用时正常脚本能执行。
func TestRunScript_Sandbox_Execute_NormalScript_Runs(t *testing.T) {
	projectDir := t.TempDir()
	scriptPath := filepath.Join(projectDir, "test.py")
	require.NoError(t, os.WriteFile(scriptPath, []byte("print('hi')"), 0644))

	rs := NewRunScriptTool()
	result, err := rs.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "python " + scriptPath,
	})
	require.NoError(t, err, "沙箱启用时工作区内正常脚本应能执行")
	assert.NotNil(t, result)
}

// ----- 沙箱网络命令 URL 预检回归测试（防 Grant 通过但 Execute 被拦的不一致）-----

// TestBash_Sandbox_GrantAndExecute_Consistent 验证 Grant 与 Execute 决策一致。
// Grant 拒绝的命令，Execute 也应拒绝（不出现 Grant 拒绝但 Execute 放行的不一致）。
func TestBash_Sandbox_GrantAndExecute_Consistent(t *testing.T) {
	projectDir := t.TempDir()
	bash := NewBashTool().(*BashTool)
	ctx := newBashSandboxCtx(t, projectDir, nil)

	cases := []struct {
		name    string
		command string
	}{
		{"危险命令 rm -rf /", "rm -rf /"},
		{"内网 curl 192.168.1.1", "curl http://192.168.1.1/"},
		{"元数据 curl 169.254.169.254", "curl http://169.254.169.254/latest/meta-data/"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Grant 应拒绝
			granted, _ := bash.Grant(ctx, map[string]any{"command": c.command})
			assert.False(t, granted, "Grant 应拒绝")

			// Execute 也应拒绝（返回 exit_code=126）
			result, err := bash.Execute(ctx, map[string]any{"command": c.command})
			require.NoError(t, err)
			m := result.(map[string]any)
			assert.Equal(t, false, m["success"], "Execute 应拦截")
			assert.Equal(t, 126, m["exit_code"])
		})
	}
}

// ----- 辅助函数 -----

// mustParseCIDRList 解析 CIDR 列表为 *net.IPNet（测试辅助）。
func mustParseCIDRList(t *testing.T, cidrs []string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		require.NoError(t, err)
		out = append(out, n)
	}
	return out
}

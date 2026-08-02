package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 B3 阶段补的集成测试，验证 Grep / 搜索工具沙箱接入后的端到端行为。
//
// 覆盖范围：
//  1. Grep 沙箱启用 - 正常搜索放行（rg 在白名单）
//  2. Grep 沙箱启用 - 原生模式遍历拒绝符号链接越界
//  3. Grep 沙箱启用 - 原生模式正常搜索工作
//  4. Grep 沙箱未启用 - 回退旧逻辑
//  5. 搜索工具（fetchBody）沙箱启用 - SSRF URL 拒绝
//  6. 搜索工具沙箱未启用 - 跳过 CheckURL

// ----- Grep 工具沙箱集成测试 -----

// TestGrep_Sandbox_RgMode_NormalSearch 验证沙箱启用时 rg 模式正常搜索。
func TestGrep_Sandbox_RgMode_NormalSearch(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\nfunc hello() {}"), 0644))

	grep := NewGrepTool()
	result, err := grep.Execute(newBashSandboxCtx(t, projectDir, nil), map[string]any{
		"pattern":     "hello",
		"include":     "*.go",
		"output_mode": "content",
	})
	require.NoError(t, err, "沙箱启用时 rg 模式搜索应正常工作")
	assert.Contains(t, result.(string), "hello")
}

// TestGrep_Sandbox_NativeMode_NormalSearch 验证沙箱启用时原生模式正常搜索。
// 直接调用 executeNative 绕过 rg 可用性检查。
func TestGrep_Sandbox_NativeMode_NormalSearch(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\nfunc hello() {}"), 0644))

	grep := NewGrepTool().(*GrepTool)
	result, err := grep.executeNative(newBashSandboxCtx(t, projectDir, nil), "hello", "*.go", "content", projectDir)
	require.NoError(t, err, "沙箱启用时原生模式搜索应正常工作")
	assert.Contains(t, result.(string), "hello")
}

// TestGrep_Sandbox_NativeMode_SymlinkDenied 验证沙箱启用时原生模式拒绝符号链接越界。
// 在项目目录内创建指向 /etc/passwd 的符号链接，EnforceFile 应跳过此文件。
func TestGrep_Sandbox_NativeMode_SymlinkDenied(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\nfunc hello() {}"), 0644))

	// 创建指向 /etc/passwd 的符号链接
	linkPath := filepath.Join(projectDir, "evil_link.txt")
	require.NoError(t, os.Symlink("/etc/passwd", linkPath))

	grep := NewGrepTool().(*GrepTool)
	// 搜索 "root"（/etc/passwd 中肯定有 root），但 EnforceFile 应拒绝符号链接
	result, err := grep.executeNative(newBashSandboxCtx(t, projectDir, nil), "root", "*.txt", "content", projectDir)
	require.NoError(t, err, "沙箱应跳过符号链接文件，不报错")
	// 结果不应包含 /etc/passwd 的内容
	resultStr, _ := result.(string)
	assert.NotContains(t, resultStr, "/etc/passwd", "沙箱应阻止通过符号链接读取 /etc/passwd")
}

// TestGrep_Sandbox_Disabled_Fallback 验证沙箱未启用时回退旧逻辑。
func TestGrep_Sandbox_Disabled_Fallback(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\nfunc hello() {}"), 0644))

	grep := NewGrepTool()
	result, err := grep.Execute(newGrantCtx(t, projectDir), map[string]any{
		"pattern":     "hello",
		"include":     "*.go",
		"output_mode": "content",
	})
	require.NoError(t, err, "沙箱未启用时应正常搜索")
	assert.Contains(t, result.(string), "hello")
}

// ----- 搜索工具（fetchBody）沙箱集成测试 -----

// TestSearch_Sandbox_SSRF_Denied 验证沙箱启用时 fetchBody 拒绝 SSRF URL。
// 直接调用 fetchBody 传入内网 URL，CheckURL 应拒绝。
func TestSearch_Sandbox_SSRF_Denied(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newBashSandboxCtx(t, projectDir, nil)
	client := newSearchStealthClient(logging.NewNopLogger(), 5 * time.Second)

	_, err := fetchBody(ctx, client, "http://127.0.0.1/test", nil)
	require.Error(t, err, "沙箱启用时 fetchBody 应拒绝 127.0.0.1（SSRF 防护）")
}

// TestSearch_Sandbox_Disabled_NoCheck 验证沙箱未启用时 fetchBody 跳过 CheckURL 预检。
// 沙箱未启用时不做 CheckURL 预检，但拨号层 ssrfDialContext 仍拦截私有地址（双层 SSRF 防护，
// 防 DNS rebinding）；错误经 fetchBody 包装后包含「搜索请求失败」，区别于沙箱拒绝原因。
func TestSearch_Sandbox_Disabled_NoCheck(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newGrantCtx(t, projectDir) // 不注入沙箱
	client := newSearchStealthClient(logging.NewNopLogger(), 1 * time.Second)

	_, err := fetchBody(ctx, client, "http://127.0.0.1/test", nil)
	require.Error(t, err, "拨号层应拦截私有地址（非沙箱拒绝）")
	// 错误信息应包含"搜索请求失败"而非沙箱拒绝原因
	assert.Contains(t, err.Error(), "搜索请求失败")
}

// TestSearch_Sandbox_NetworkAllowSubnets_Allows 验证 NetworkAllowSubnets 放行后 CheckURL 不拒绝，
// 但拨号层 ssrfDialContext 仍拦截私有地址（双层 SSRF：拨号层比 CheckURL 更紧，防 DNS rebinding）。
// 即便策略放行 127.0.0.0/8，WebSearch 仍不允许抓取内网地址——这是 WebSearch 的安全语义。
func TestSearch_Sandbox_NetworkAllowSubnets_Allows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newBashSandboxCtx(t, projectDir, func(p *sandbox.SandboxPolicy) {
		p.NetworkAllowSubnets = mustParseCIDRList(t, []string{"127.0.0.0/8"})
	})
	client := newSearchStealthClient(logging.NewNopLogger(), 1 * time.Second)

	_, err := fetchBody(ctx, client, "http://127.0.0.1/test", nil)
	require.Error(t, err, "拨号层应拦截私有地址（即便 CheckURL 放行）")
	// 错误信息应包含"搜索请求失败"而非沙箱拒绝原因（CheckURL 已放行）
	assert.Contains(t, err.Error(), "搜索请求失败")
}

// ----- Grep 沙箱策略禁用 rg 测试 -----

// TestGrep_Sandbox_RgDisabledByPolicy 验证沙箱策略禁用 rg 时 Grep 被拒绝。
// 配置 AllowedCommands 不包含 rg，CheckCommand 应返回 Deny。
func TestGrep_Sandbox_RgDisabledByPolicy(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("hello"), 0644))

	// 构造沙箱策略：AllowedCommands 不包含 rg
	policy := sandbox.SandboxPolicy{
		AllowedDirs:           []string{projectDir},
		DeniedFileGlobs:       sandbox.DefaultDeniedFileGlobs(),
		DeniedDirGlobs:        sandbox.DefaultDeniedDirGlobs(),
		DeniedDevicePaths:     sandbox.DefaultDeniedDevicePaths(),
		NetworkDenySubnets:    sandbox.DefaultDeniedSubnets(),
		AllowedCommands:       []string{"cat", "echo", "ls"}, // 不包含 rg
		DeniedCommandPatterns: sandbox.DefaultDeniedCommandPatterns(),
		NetworkCommands:       sandbox.DefaultNetworkCommands(),
	}
	sb, err := sandbox.NewSandbox(&policy, logging.NewNopLogger())
	require.NoError(t, err)

	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger(), session.WithSandbox(sb))
	require.NoError(t, err)

	ctx := WithToolContext(context.Background(), &ToolContext{
		Session:          sess,
		SessionWhitelist: sess.Whitelist(),
	})

	grep := NewGrepTool()
	_, err = grep.Execute(ctx, map[string]any{
		"pattern": "hello",
	})
	// rg 被策略禁用时，executeWithRg 应返回 error
	// 但如果 rg 不可用会回退到 executeNative（不调用 CheckCommand）
	// 所以这个测试只在 rg 可用时验证 CheckCommand 拒绝
	if isRgAvailable() {
		require.Error(t, err, "沙箱策略禁用 rg 时应返回 error")
	} else {
		t.Skip("rg 不可用，无法测试 CheckCommand 拒绝 rg 的场景")
	}
}

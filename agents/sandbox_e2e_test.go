package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 B4 阶段的端到端测试，验证沙箱通过 Runtime 自动注入到所有会话（含子 Agent 会话）。
//
// 覆盖范围：
//  1. WithSandbox 注入后 Runtime.Sandbox() 返回沙箱实例
//  2. 未注入时 Runtime.Sandbox() 返回 nil
//  3. SessionConfigs() 在沙箱已配置时返回 Compactor + Sandbox
//  4. SessionConfigs() 在沙箱未配置时返回仅 Compactor（始终非 nil）
//  5. 子 Agent 会话自动注入沙箱（通过 subAgents.getOrCreate 验证）
//  6. 主会话通过 rt.SessionConfigs() 统一注入

// newTestSandbox 创建用于测试的沙箱实例。
func newTestSandbox(t *testing.T, projectDir string) *sandbox.Sandbox {
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
	sb, err := sandbox.NewSandbox(&policy, logging.NewNopLogger())
	require.NoError(t, err)
	return sb
}

// TestRuntime_Sandbox_NilWhenNotSet 验证未注入沙箱时 Sandbox() 返回 nil。
func TestRuntime_Sandbox_NilWhenNotSet(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	assert.Nil(t, rt.Sandbox(), "未注入沙箱时 Sandbox() 应返回 nil")
}

// TestRuntime_Sandbox_ReturnsInstance 验证 WithSandbox 注入后 Sandbox() 返回同一实例。
func TestRuntime_Sandbox_ReturnsInstance(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	assert.Equal(t, sb, rt.Sandbox(), "Sandbox() 应返回注入的沙箱实例")
}

// TestRuntime_SessionConfigs_AlwaysContainsCompactor 验证 SessionConfigs 始终返回含 Compactor 的配置。
// 即使沙箱未配置，Compactor（压缩引擎）作为 Runtime 内置能力也必须注入。
func TestRuntime_SessionConfigs_AlwaysContainsCompactor(t *testing.T) {
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	opts := rt.SessionConfigs()
	require.NotNil(t, opts, "SessionConfigs 应始终返回非 nil（至少含 Compactor）")
	assert.Len(t, opts, 1, "沙箱未配置时应只含 Compactor")
}

// TestRuntime_SessionConfigs_ContainsSandboxWhenSet 验证沙箱已配置时 SessionConfigs 同时含 Compactor 和 Sandbox。
func TestRuntime_SessionConfigs_ContainsSandboxWhenSet(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	opts := rt.SessionConfigs()
	require.NotNil(t, opts, "SessionConfigs 应返回非 nil")
	assert.Len(t, opts, 2, "沙箱已配置时应含 Compactor + Sandbox 共 2 个 SessionConfig")
}

// TestRuntime_SubAgentSession_AutoInjectsSandbox 验证子 Agent 会话自动注入沙箱。
// 通过 subAgents.getOrCreate 创建子会话，验证沙箱字段已注入。
func TestRuntime_SubAgentSession_AutoInjectsSandbox(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	store := newFakeSessionStore()

	st, err := rt.subAgents.getOrCreate(context.Background(), "sub-agent", projectDir, "", store, "")
	require.NoError(t, err, "子会话应创建成功")
	require.NotNil(t, st, "子会话状态应非空")
	assert.Equal(t, sb, st.sess.Sandbox(), "子会话应自动注入沙箱")
}

// TestRuntime_SubAgentSession_NoSandboxWhenNotSet 验证未配置沙箱时子会话不注入。
func TestRuntime_SubAgentSession_NoSandboxWhenNotSet(t *testing.T) {
	projectDir := t.TempDir()
	rt := NewRuntime(WithLogger(logging.NewNopLogger()))
	store := newFakeSessionStore()

	st, err := rt.subAgents.getOrCreate(context.Background(), "sub-agent", projectDir, "", store, "")
	require.NoError(t, err, "子会话应创建成功")
	require.NotNil(t, st, "子会话状态应非空")
	assert.Nil(t, st.sess.Sandbox(), "未配置沙箱时子会话不应注入沙箱")
}

// TestRuntime_SubAgentSession_SandboxReusedAcrossSessions 验证同一 Runtime 创建的多个子会话共享同一沙箱实例。
func TestRuntime_SubAgentSession_SandboxReusedAcrossSessions(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	store := newFakeSessionStore()

	st1, err1 := rt.subAgents.getOrCreate(context.Background(), "agent-a", projectDir, "", store, "")
	st2, err2 := rt.subAgents.getOrCreate(context.Background(), "agent-b", projectDir, "", store, "")
	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NotNil(t, st1)
	require.NotNil(t, st2)
	assert.Equal(t, sb, st1.sess.Sandbox(), "agent-a 会话应注入沙箱")
	assert.Equal(t, sb, st2.sess.Sandbox(), "agent-b 会话应注入同一沙箱实例")
}

// TestRuntime_MainSession_ManualInject 验证主会话通过 rt.Sandbox() 手动注入。
// 这是 mindx handler_session.go 创建主会话时的预期用法。
func TestRuntime_MainSession_ManualInject(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	store := newFakeSessionStore()

	// 模拟 mindx handler_session.go 的主会话创建：
	// sess, _ := session.New(..., session.WithSandbox(rt.Sandbox()))
	sess, err := session.New("main-agent", "", projectDir, store, logging.NewNopLogger(),
		session.WithSandbox(rt.Sandbox()),
	)
	require.NoError(t, err)
	assert.Equal(t, sb, sess.Sandbox(), "主会话应通过 rt.Sandbox() 注入沙箱")
}

// TestRuntime_SandboxEndToEnd_ToolEnforcement 验证沙箱通过 Runtime 注入后，
// 工具在端到端流程中能正确执行沙箱决策。
// 这是 B1-B3 工具沙箱接入与 B4 Runtime 注入的集成验证。
func TestRuntime_SandboxEndToEnd_ToolEnforcement(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, projectDir)
	rt := NewRuntime(WithLogger(logging.NewNopLogger()), WithSandbox(sb))
	store := newFakeSessionStore()

	// 创建带沙箱的会话（模拟 mindx 主会话创建路径）
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger(),
		session.WithSandbox(rt.Sandbox()),
	)
	require.NoError(t, err)

	// 验证 Read 工具在沙箱启用时拒绝敏感文件
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	read := tools.NewReadTool()
	ctx := tools.WithToolContext(context.Background(), &tools.ToolContext{
		Session:          sess,
		SessionWhitelist: sess.Whitelist(),
		Logger:           rt.Logger(),
	})

	// Grant 阶段应拒绝敏感文件
	granted, reason := read.Grant(ctx, map[string]any{"filePath": envFile})
	assert.False(t, granted, "端到端：沙箱启用时 .env 应被 Grant 拒绝")
	assert.Contains(t, reason, "敏感")

	// Execute 阶段也应拒绝
	_, err = read.Execute(ctx, map[string]any{"filePath": envFile})
	require.Error(t, err, "端到端：沙箱启用时 .env 应被 Execute 拒绝")
	assert.Contains(t, err.Error(), "敏感")
}

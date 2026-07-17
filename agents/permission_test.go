package agents

import (
	"context"
	"testing"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecurityLevelString 验证安全级别字符串映射。
func TestSecurityLevelString(t *testing.T) {
	assert.Equal(t, "safe", securityLevelString(events.LevelSafe))
	assert.Equal(t, "sensitive", securityLevelString(events.LevelSensitive))
	assert.Equal(t, "high_risk", securityLevelString(events.LevelHighRisk))
	assert.Equal(t, "unknown", securityLevelString(events.SecurityLevel(999)))
}

// TestBuildWhitelistEntryBash 验证 bash 命令提取基础命令名。
func TestBuildWhitelistEntryBash(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "bash",
		Arguments: map[string]any{"command": "git status -s"},
	}
	assert.Equal(t, "git", rt.buildWhitelistEntry(pending, sess))
}

// TestBuildWhitelistEntryBashEmpty 验证空 bash 命令返回空字符串。
func TestBuildWhitelistEntryBashEmpty(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "bash",
		Arguments: map[string]any{},
	}
	assert.Empty(t, rt.buildWhitelistEntry(pending, sess))
}

// TestBuildWhitelistEntryWrite 验证 write 路径解析为绝对路径。
func TestBuildWhitelistEntryWrite(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "write",
		Arguments: map[string]any{"filePath": "foo.txt"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.NotEmpty(t, entry)
	assert.True(t, len(entry) > 0 && entry[0] == '/')
}

// TestBuildWhitelistEntryRunScript 验证 run_script 脚本路径解析。
func TestBuildWhitelistEntryRunScript(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "run_script",
		Arguments: map[string]any{"command": "python script.py", "working_dir": "/tmp"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.Contains(t, entry, "script.py")
}

// TestCheckPermissionGrantsAllGranted 验证所有工具都授权时返回 nil。
func TestCheckPermissionGrantsAllGranted(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	fake := newFakeTool("Restricted", func(_ context.Context, _ map[string]any) (bool, string) {
		return true, ""
	})
	fake.info.SecurityLevel = events.LevelSensitive
	require.NoError(t, rt.toolReg.Register(fake))

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{ID: "tc1", Name: "Restricted", Arguments: map[string]any{}}}
	pending := rt.checkPermissionGrants(context.Background(), b, invocs, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.Nil(t, pending)
}

// TestCheckPermissionGrantsDenied 验证拒绝时设置 PendingPermission 并发送事件。
func TestCheckPermissionGrantsDenied(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	fake := newFakeTool("Restricted", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	fake.info.SecurityLevel = events.LevelSensitive
	require.NoError(t, rt.toolReg.Register(fake))

	b := rt.Ask("test-agent", "hi", sess)
	var emitted bool
	emit := func(_ events.ReactEventType, _ any) { emitted = true }
	invocs := []hooks.ToolCallInvocation{{ID: "tc1", Name: "Restricted", Arguments: map[string]any{}}}
	pending := rt.checkPermissionGrants(context.Background(), b, invocs, emit, logging.NewNopLogger())

	require.NotNil(t, pending)
	assert.Equal(t, "Restricted", pending.ToolName)
	assert.Equal(t, "tc1", pending.ToolCallID)
	assert.True(t, emitted)
	assert.NotNil(t, sess.TakePendingPermission())
}

// TestCheckPermissionGrantsNonPermissionTool 验证未实现 PermissionRequired 的工具被跳过。
func TestCheckPermissionGrantsNonPermissionTool(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	require.NoError(t, rt.toolReg.Register(newFakeTool("SafeTool", nil)))

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{ID: "tc1", Name: "SafeTool"}}
	pending := rt.checkPermissionGrants(context.Background(), b, invocs, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.Nil(t, pending)
}

// TestResolvePermissionMagicWordAllow 验证 Allow 魔法词执行挂起的工具。
func TestResolvePermissionMagicWordAllow(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	fake := newFakeTool("Restricted", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	fake.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return "allowed result", nil
	}
	require.NoError(t, rt.toolReg.Register(fake))

	sess.SetPendingPermission(session.PendingPermission{
		ToolName:   "Restricted",
		ToolCallID: "tc1",
		Arguments:  map[string]any{},
	})

	b := rt.Ask("test-agent", tools.PermissionAllow, sess)
	consumed := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.True(t, consumed)
	assert.Nil(t, sess.TakePendingPermission())

	msgs := sess.All()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tool", msgs[0].Role)
	assert.Equal(t, "allowed result", msgs[0].Content)
}

// TestResolvePermissionMagicWordDeny 验证 Deny 魔法词追加拒绝结果。
func TestResolvePermissionMagicWordDeny(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)

	sess.SetPendingPermission(session.PendingPermission{
		ToolName:   "Restricted",
		ToolCallID: "tc1",
		Reason:     "危险操作",
		Arguments:  map[string]any{},
	})

	b := rt.Ask("test-agent", tools.PermissionDeny, sess)
	consumed := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.True(t, consumed)
	assert.Nil(t, sess.TakePendingPermission())

	msgs := sess.All()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tool", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "权限被拒绝")
}

// TestResolvePermissionMagicWordNoPending 验证无挂起时魔法词按普通消息处理。
func TestResolvePermissionMagicWordNoPending(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := rt.Ask("test-agent", tools.PermissionAllow, sess)
	consumed := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.False(t, consumed)
}

// TestResolvePermissionMagicWordRegularMessage 验证普通消息不被消费。
func TestResolvePermissionMagicWordRegularMessage(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := rt.Ask("test-agent", "hello", sess)
	consumed := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.False(t, consumed)
}

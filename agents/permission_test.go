package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/config"
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

// TestBuildWhitelistEntryWriteSnakeCase 验证 write 使用 snake_case file_path 时同样解析为绝对路径。
func TestBuildWhitelistEntryWriteSnakeCase(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "write",
		Arguments: map[string]any{"file_path": "foo.txt"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.NotEmpty(t, entry)
	assert.True(t, len(entry) > 0 && entry[0] == '/')
}

// TestBuildWhitelistEntryEditSnakeCase 验证 edit 使用 snake_case file_path（Edit 的 schema 键）时解析为绝对路径。
func TestBuildWhitelistEntryEditSnakeCase(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "edit",
		Arguments: map[string]any{"file_path": "foo.txt"},
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

// TestBuildWhitelistEntryRunScriptDefaultWorkingDir 验证 run_script 未传 working_dir 时，
// 默认以项目目录为基准解析脚本路径（修复相对 working_dir 下白名单条目永不命中的缺陷）。
func TestBuildWhitelistEntryRunScriptDefaultWorkingDir(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "run_script",
		Arguments: map[string]any{"command": "python script.py"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.Contains(t, entry, "script.py")
	assert.True(t, strings.HasPrefix(entry, sess.ProjectDir()+string(filepath.Separator)),
		"默认 working_dir 应以项目目录为基准，得到: %s", entry)
}

// TestBuildWhitelistEntryRead 验证 read 的 filePath 解析为绝对路径（与 write/edit 一致）。
func TestBuildWhitelistEntryRead(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "read",
		Arguments: map[string]any{"filePath": "foo.txt"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.NotEmpty(t, entry)
	assert.True(t, len(entry) > 0 && entry[0] == '/')
}

// TestBuildWhitelistEntryReadEmpty 验证 read 缺参数返回空字符串（不做白名单）。
func TestBuildWhitelistEntryReadEmpty(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "read",
		Arguments: map[string]any{},
	}
	assert.Empty(t, rt.buildWhitelistEntry(pending, sess))
}

// TestBuildWhitelistEntryLs 验证 ls 的 path 解析为绝对路径。
func TestBuildWhitelistEntryLs(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "ls",
		Arguments: map[string]any{"path": "subdir"},
	}
	entry := rt.buildWhitelistEntry(pending, sess)
	assert.NotEmpty(t, entry)
	assert.True(t, len(entry) > 0 && entry[0] == '/')
}

// TestBuildWhitelistEntryLsEmpty 验证 ls 缺参数返回空字符串（不做白名单）。
func TestBuildWhitelistEntryLsEmpty(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	pending := &session.PendingPermission{
		ToolName:  "ls",
		Arguments: map[string]any{},
	}
	assert.Empty(t, rt.buildWhitelistEntry(pending, sess))
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
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
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
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.True(t, consumed)
	assert.Nil(t, sess.TakePendingPermission())

	msgs := sess.All()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tool", msgs[0].Role)
	// 拒绝结果采用引导式话术：明确"不允许授权"并提示调整执行思路
	assert.Contains(t, msgs[0].Content, "不允许授权")
	assert.Contains(t, msgs[0].Content, "考虑其它的执行路径或工具")
	assert.Equal(t, "tc1", msgs[0].ToolCallID)
}

// TestResolvePermissionMagicWordAllowWithImage 验证授权路径（PermissionAllow）
// 真正执行工具后，若工具返回图片（如白名单外文件的图片读取），
// 图片同样被转换为 image_url 视觉消息追加到会话（与正常执行路径一致），
// 不会在授权路径上被静默丢弃。
func TestResolvePermissionMagicWordAllowWithImage(t *testing.T) {
	rt := newTestRuntimeWithTools(t, nil,
		WithModel(config.ModelConfig{Name: "m", Provider: "mock", Visioning: true}))
	sess := newTestSession(t)

	fake := newFakeTool("Read", func(_ context.Context, _ map[string]any) (bool, string) {
		return false, "需要用户确认"
	})
	fake.execute = func(_ context.Context, _ map[string]any) (any, error) {
		return &tools.ReadResult{
			Data: &tools.ReadData{
				Success: true, Path: "/tmp/a.png", Content: "图片摘要：512x300",
			},
			Images: []tools.ImageContent{
				{MediaType: "image/png", Base64Data: "aGVsbG8=", Width: 512, Height: 300},
			},
		}, nil
	}
	require.NoError(t, rt.toolReg.Register(fake))

	sess.SetPendingPermission(session.PendingPermission{
		ToolName:   "Read",
		ToolCallID: "tc1",
		Arguments:  map[string]any{"filePath": "/tmp/a.png"},
	})

	b := rt.Ask("test-agent", tools.PermissionAllow, sess)
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.True(t, consumed)

	msgs := sess.All()
	require.Len(t, msgs, 2)
	assert.Equal(t, "tool", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "图片摘要：512x300")
	// 授权路径的图片消息：user 角色、携带图片块、位于工具结果之后
	imgMsg := msgs[1]
	assert.Equal(t, "user", imgMsg.Role)
	require.Len(t, imgMsg.Images, 1)
	assert.Equal(t, "image/png", imgMsg.Images[0].MediaType)
	assert.Equal(t, "aGVsbG8=", imgMsg.Images[0].Base64Data)
	// base64 不应混入工具结果文本
	assert.NotContains(t, msgs[0].Content, "aGVsbG8=")
}

// TestResolvePermissionMagicWordNoPending 验证无挂起时魔法词按普通消息处理。
func TestResolvePermissionMagicWordNoPending(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := rt.Ask("test-agent", tools.PermissionAllow, sess)
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.False(t, consumed)
}

// TestResolvePermissionMagicWordRegularMessage 验证普通消息不被消费。
func TestResolvePermissionMagicWordRegularMessage(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := rt.Ask("test-agent", "hello", sess)
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b, rt.toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	assert.False(t, consumed)
}

// TestResolvePermissionMagicWordReadAllowSession 端到端验证真实 Read 工具：
//  1. 越界读取 → checkPermissionGrants 捕获授权（granted=false → pending）
//  2. 用户以 PermissionAllowSession 响应 → 真正执行工具并写入会话白名单
//  3. 同一路径再次 Grant → 直接放行（无需再次授权）
func TestResolvePermissionMagicWordReadAllowSession(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("外部机密内容"), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewReadTool()})

	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	// 1. 越界读取 → 触发授权
	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{
		ID: "tc1", Name: "Read",
		Arguments: map[string]any{"filePath": outsideFile},
	}}
	pending := rt.checkPermissionGrants(context.Background(), b, invocs, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	require.NotNil(t, pending, "越界读取应触发授权流程")
	require.Equal(t, "Read", pending.ToolName)

	// 2. PermissionAllowSession → 写入会话白名单并执行
	sess.SetPendingPermission(*pending)
	// 与正常流程一致，按 Ask 构建带 session 的专用工具执行器
	toolExec := tools.NewToolExecutor(rt.toolReg,
		tools.WithLogger(logging.NewNopLogger()),
		tools.WithSession(sess),
		tools.WithSessionStore(rt.sessionStore),
	)
	b2 := rt.Ask("test-agent", tools.PermissionAllowSession, sess)
	consumed, _ := rt.resolvePermissionMagicWord(context.Background(), b2, toolExec, func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())
	require.True(t, consumed)

	// 3. 白名单已写入：同一路径再次 Grant → 直接放行
	readTool := tools.NewReadTool()
	granted, _ := readTool.Grant(rt.buildGrantToolContext(context.Background(), sess), map[string]any{"filePath": outsideFile})
	require.True(t, granted, "会话白名单内的路径应直接放行")

	// 工具结果应包含文件内容（授权后真正执行成功）
	msgs := sess.All()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tool", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "外部机密内容")
}

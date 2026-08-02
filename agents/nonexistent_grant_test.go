package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadGrant_NonExistentFile_OutsideWorkspace 验证：
// 对工作区外不存在的文件，Read.Grant 必须返回 granted=true（不触发授权）。
// 这是上一轮修复"先弹窗后检查存在性"问题的核心断言。
func TestReadGrant_NonExistentFile_OutsideWorkspace(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	// 关键：文件不存在
	nonExistentFile := filepath.Join(outsideDir, "never_exists_xyz.txt")

	// 前提断言：文件确实不存在
	_, statErr := os.Stat(nonExistentFile)
	require.True(t, os.IsNotExist(statErr), "测试前提：文件不应该存在")

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewReadTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{
		ID: "tc1", Name: "Read",
		Arguments: map[string]any{"filePath": nonExistentFile},
	}}

	pending := rt.checkPermissionGrants(context.Background(), b, invocs,
		func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())

	// 核心断言：不存在的文件不应该触发授权
	assert.Nil(t, pending, "对不存在的文件不应触发权限请求，但触发了：reason=%v", pending)
}

// TestReadGrant_NonExistentFile_InsideWorkspace 验证：
// 对工作区内不存在的文件，Read.Grant 必须返回 granted=true。
func TestReadGrant_NonExistentFile_InsideWorkspace(t *testing.T) {
	projectDir := t.TempDir()
	nonExistentFile := filepath.Join(projectDir, "never_exists_inside.txt")

	_, statErr := os.Stat(nonExistentFile)
	require.True(t, os.IsNotExist(statErr))

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewReadTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	readTool := tools.NewReadTool()
	grantCtx := rt.buildGrantToolContext(context.Background(), sess)

	granted, reason := readTool.Grant(grantCtx, map[string]any{"filePath": nonExistentFile})
	assert.True(t, granted, "对工作区内不存在的文件应放行，但被拒绝：reason=%s", reason)
}

// TestReadGrant_ExistingFile_OutsideWorkspace 验证：
// 对工作区外存在的文件，Read.Grant 必须返回 granted=false（触发授权）。
// 这是对照测试，确认"存在性检查"没有破坏正常的越界授权。
func TestReadGrant_ExistingFile_OutsideWorkspace(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	existingFile := filepath.Join(outsideDir, "exists.txt")
	require.NoError(t, os.WriteFile(existingFile, []byte("content"), 0644))

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewReadTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{
		ID: "tc1", Name: "Read",
		Arguments: map[string]any{"filePath": existingFile},
	}}

	pending := rt.checkPermissionGrants(context.Background(), b, invocs,
		func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())

	require.NotNil(t, pending, "对工作区外存在的文件应触发授权")
	assert.Equal(t, "Read", pending.ToolName)
}

// TestEditGrant_NonExistentFile 验证 Edit 对不存在的文件不触发授权
func TestEditGrant_NonExistentFile(t *testing.T) {
	projectDir := t.TempDir()
	nonExistentFile := filepath.Join(projectDir, "edit_never_exists.txt")

	_, statErr := os.Stat(nonExistentFile)
	require.True(t, os.IsNotExist(statErr))

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewEditTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	editTool := tools.NewEditTool()
	grantCtx := rt.buildGrantToolContext(context.Background(), sess)

	granted, reason := editTool.Grant(grantCtx, map[string]any{
		"file_path":  nonExistentFile,
		"old_string": "foo",
		"new_string": "bar",
	})
	assert.True(t, granted, "对不存在的文件 Edit 应放行，但被拒绝：reason=%s", reason)
}

// TestLsGrant_NonExistentDir 验证 Ls 对不存在的目录不触发授权
func TestLsGrant_NonExistentDir(t *testing.T) {
	projectDir := t.TempDir()
	nonExistentDir := filepath.Join(projectDir, "never_exists_dir")

	_, statErr := os.Stat(nonExistentDir)
	require.True(t, os.IsNotExist(statErr))

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewLsTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{
		ID: "tc1", Name: "Ls",
		Arguments: map[string]any{"path": nonExistentDir},
	}}

	pending := rt.checkPermissionGrants(context.Background(), b, invocs,
		func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())

	assert.Nil(t, pending, "对不存在的目录 Ls 不应触发权限请求，但触发了：reason=%v", pending)
}

// TestReadGrant_ENOTDIR 验证：当路径中间段是文件而非目录时（ENOTDIR），
// os.Stat 返回非 IsNotExist 错误，但文件不可能存在，Grant 必须放行。
// 这是上一轮修复遗漏的关键场景：旧代码用 os.IsNotExist 判断，ENOTDIR 不命中导致误弹授权。
func TestReadGrant_ENOTDIR(t *testing.T) {
	projectDir := t.TempDir()
	// 在项目外创建一个真实文件
	outsideDir := t.TempDir()
	realFile := filepath.Join(outsideDir, "realfile.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("hi"), 0644))

	// 关键路径：/path/to/realfile.txt/sub/file.txt
	// realfile.txt 是文件而不是目录 → os.Stat 返回 ENOTDIR（不是 IsNotExist）
	enotdirPath := filepath.Join(realFile, "sub", "file.txt")

	_, statErr := os.Stat(enotdirPath)
	require.True(t, statErr != nil, "ENOTDIR 路径必须返回错误")
	require.False(t, os.IsNotExist(statErr), "ENOTDIR 不应命中 IsNotExist——这是 bug 的触发条件")

	rt := newTestRuntimeWithTools(t, []tools.FuncTool{tools.NewReadTool()})
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger())
	require.NoError(t, err)
	store.ensureMeta(sess)

	b := rt.Ask("test-agent", "hi", sess)
	invocs := []hooks.ToolCallInvocation{{
		ID: "tc1", Name: "Read",
		Arguments: map[string]any{"filePath": enotdirPath},
	}}

	pending := rt.checkPermissionGrants(context.Background(), b, invocs,
		func(_ events.ReactEventType, _ any) {}, logging.NewNopLogger())

	assert.Nil(t, pending, "ENOTDIR 路径文件不可能存在，不应触发权限请求，但触发了：reason=%v", pending)
}


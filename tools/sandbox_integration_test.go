package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 B1 审计阶段补的集成测试，验证沙箱接入后各工具的端到端行为。
//
// 覆盖范围：
//   1. 沙箱启用 - 不存在的文件 → Grant 放行（不弹窗，让 Execute 报错）
//   2. 沙箱启用 - 工作区内文件 → Grant 放行
//   3. 沙箱启用 - 越界文件 → Grant 触发授权（granted=false）
//   4. 沙箱启用 - 敏感文件（.env）→ Grant 拒绝（granted=false，原因含"敏感"）
//   5. 沙箱启用 - 工具白名单内越界 → Grant 放行
//   6. 沙箱启用 - 会话级白名单内越界 → Grant 放行
//   7. 沙箱启用 - Execute 阶段 EnforceFile 拒绝敏感文件
//   8. 沙箱未启用 - 回退到旧逻辑（向后兼容）
//   9. Glob 工具沙箱启用 - 越界直接 Deny（Execute 返回 error）
//
// 工具覆盖：Read / Edit / Write / Ls / Glob / RunScript。

// ----- 测试辅助函数 -----

// newSandboxCtx 构造带沙箱的会话上下文。
// policy 为 nil 时使用默认策略 + 指定 AllowedDirs。
func newSandboxCtx(t *testing.T, projectDir string, policy *sandbox.SandboxPolicy) context.Context {
	t.Helper()
	if policy == nil {
		p := sandbox.SandboxPolicy{
			AllowedDirs:        []string{projectDir},
			DeniedFileGlobs:    sandbox.DefaultDeniedFileGlobs(),
			DeniedDirGlobs:     sandbox.DefaultDeniedDirGlobs(),
			DeniedDevicePaths:  sandbox.DefaultDeniedDevicePaths(),
		}
		policy = &p
	}
	sb, err := sandbox.NewSandbox(policy, logging.NewNopLogger())
	require.NoError(t, err)

	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", projectDir, store, logging.NewNopLogger(), session.WithSandbox(sb))
	require.NoError(t, err)

	return WithToolContext(context.Background(), &ToolContext{
		Session:          sess,
		SessionWhitelist: sess.Whitelist(),
		Logger:           logging.NewNopLogger(),
		EmitEvent:        func(e events.ReactEvent) {},
	})
}

// ----- Read 工具沙箱集成测试 -----

// TestRead_Sandbox_NonExistent_Allows 验证沙箱启用时对不存在的文件 Grant 放行。
// 这是上次踩坑的核心场景：原代码先检查权限再检查存在性，导致对不存在的文件弹授权窗。
func TestRead_Sandbox_NonExistent_Allows(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists.txt")

	read := NewReadTool()
	granted, _ := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": nonExistent})
	assert.True(t, granted, "沙箱启用时对不存在的文件应放行，不弹授权窗")
}

// TestRead_Sandbox_InWorkspace_Allows 验证沙箱启用时工作区内文件 Grant 放行。
func TestRead_Sandbox_InWorkspace_Allows(t *testing.T) {
	projectDir := t.TempDir()
	inWorkspace := filepath.Join(projectDir, "main.go")
	require.NoError(t, os.WriteFile(inWorkspace, []byte("package main"), 0644))

	read := NewReadTool()
	granted, _ := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": inWorkspace})
	assert.True(t, granted, "沙箱启用时工作区内文件应放行")
}

// TestRead_Sandbox_Outside_AsksUser 验证沙箱启用时越界文件 Grant 触发授权。
func TestRead_Sandbox_Outside_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	read := NewReadTool()
	granted, reason := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": outsideFile})
	assert.False(t, granted, "沙箱启用时越界文件应触发授权")
	assert.Contains(t, reason, "工作区")
}

// TestRead_Sandbox_SensitiveFile_Denies 验证沙箱启用时敏感文件 Grant 拒绝。
func TestRead_Sandbox_SensitiveFile_Denies(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	read := NewReadTool()
	granted, reason := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": envFile})
	assert.False(t, granted, "沙箱启用时敏感文件应被拒绝")
	assert.Contains(t, reason, "敏感")
}

// TestRead_Sandbox_ToolWhitelist_Allows 验证沙箱启用时工具白名单内越界 Grant 放行。
func TestRead_Sandbox_ToolWhitelist_Allows(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	read := NewReadTool()
	read.AddWhiteList(outsideDir)
	granted, _ := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": outsideFile})
	assert.True(t, granted, "沙箱启用时工具白名单内越界文件应放行")
}

// TestRead_Sandbox_SessionWhitelist_Allows 验证沙箱启用时会话级白名单内越界 Grant 放行。
func TestRead_Sandbox_SessionWhitelist_Allows(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	ctx := newSandboxCtx(t, projectDir, nil)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("read", outsideDir))

	read := NewReadTool()
	granted, _ := read.Grant(ctx, map[string]any{"filePath": outsideFile})
	assert.True(t, granted, "沙箱启用时会话级白名单内越界文件应放行")
}

// TestRead_Sandbox_Execute_EnforceSensitiveFile 验证 Execute 阶段 EnforceFile 拒绝敏感文件。
// 即便 Grant 阶段因某种原因放行（如策略热更新后变为敏感），Execute 仍能拦截。
func TestRead_Sandbox_Execute_EnforceSensitiveFile(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	read := NewReadTool()
	_, err := read.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": envFile})
	require.Error(t, err, "沙箱启用时 Execute 阶段应拒绝敏感文件")
	assert.Contains(t, err.Error(), "敏感")
}

// TestRead_Sandbox_Disabled_FallbackToOldLogic 验证沙箱未启用时回退到旧逻辑。
// 与现有 TestRead_Grant 行为等价：越界触发授权。
func TestRead_Sandbox_Disabled_FallbackToOldLogic(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	// newGrantCtx 不注入沙箱，等价于沙箱未启用
	read := NewReadTool()
	granted, reason := read.Grant(newGrantCtx(t, projectDir), map[string]any{"filePath": outsideFile})
	assert.False(t, granted, "沙箱未启用时越界文件应触发授权（旧逻辑）")
	assert.Contains(t, reason, "工作区之外")
}

// ----- Edit 工具沙箱集成测试 -----

// TestEdit_Sandbox_NonExistent_Allows 验证沙箱启用时对不存在的文件 Grant 放行。
// Edit 对不存在的文件永远报错不创建，Grant 放行让 Execute 报错。
func TestEdit_Sandbox_NonExistent_Allows(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists.go")

	edit := NewEditTool()
	granted, _ := edit.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"file_path":   nonExistent,
		"old_string": "a",
		"new_string": "b",
	})
	assert.True(t, granted, "沙箱启用时对不存在的文件应放行")
}

// TestEdit_Sandbox_Outside_AsksUser 验证沙箱启用时越界文件 Grant 触发授权。
func TestEdit_Sandbox_Outside_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	edit := NewEditTool()
	granted, _ := edit.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"file_path":   outsideFile,
		"old_string": "hi",
		"new_string": "hello",
	})
	assert.False(t, granted, "沙箱启用时越界文件应触发授权")
}

// TestEdit_Sandbox_SensitiveFile_Denies 验证沙箱启用时敏感文件 Grant 拒绝。
func TestEdit_Sandbox_SensitiveFile_Denies(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	edit := NewEditTool()
	granted, reason := edit.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"file_path":   envFile,
		"old_string": "SECRET",
		"new_string": "TOKEN",
	})
	assert.False(t, granted, "沙箱启用时敏感文件应被拒绝")
	assert.Contains(t, reason, "敏感")
}

// TestEdit_Sandbox_Execute_EnforceSensitiveFile 验证 Execute 阶段拒绝敏感文件。
func TestEdit_Sandbox_Execute_EnforceSensitiveFile(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	edit := NewEditTool()
	_, err := edit.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"file_path":   envFile,
		"old_string": "SECRET",
		"new_string": "TOKEN",
	})
	require.Error(t, err, "沙箱启用时 Execute 阶段应拒绝敏感文件")
	assert.Contains(t, err.Error(), "敏感")
}

// ----- Write 工具沙箱集成测试 -----

// TestWrite_Sandbox_InWorkspace_Allows 验证沙箱启用时工作区内文件 Grant 放行（含创建场景）。
func TestWrite_Sandbox_InWorkspace_Allows(t *testing.T) {
	projectDir := t.TempDir()
	newFile := filepath.Join(projectDir, "new.txt")

	write := NewWriteTool()
	granted, _ := write.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"filePath": newFile,
		"content":  "hello",
	})
	assert.True(t, granted, "沙箱启用时工作区内新文件应放行")
}

// TestWrite_Sandbox_Outside_AsksUser 验证沙箱启用时越界文件 Grant 触发授权。
func TestWrite_Sandbox_Outside_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	write := NewWriteTool()
	granted, _ := write.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"filePath": outsideFile,
		"content":  "hello",
	})
	assert.False(t, granted, "沙箱启用时越界文件应触发授权")
}

// TestWrite_Sandbox_SensitiveFile_Denies 验证沙箱启用时敏感文件 Grant 拒绝。
func TestWrite_Sandbox_SensitiveFile_Denies(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	write := NewWriteTool()
	granted, reason := write.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"filePath": envFile,
		"content":  "new",
	})
	assert.False(t, granted, "沙箱启用时敏感文件应被拒绝")
	assert.Contains(t, reason, "敏感")
}

// TestWrite_Sandbox_Execute_EnforceSensitiveFile 验证 Execute 阶段拒绝敏感文件。
func TestWrite_Sandbox_Execute_EnforceSensitiveFile(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	write := NewWriteTool()
	_, err := write.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"filePath": envFile,
		"content":  "new",
	})
	require.Error(t, err, "沙箱启用时 Execute 阶段应拒绝敏感文件")
	assert.Contains(t, err.Error(), "敏感")
}

// ----- Ls 工具沙箱集成测试 -----

// TestLs_Sandbox_NonExistent_Allows 验证沙箱启用时对不存在的目录 Grant 放行。
func TestLs_Sandbox_NonExistent_Allows(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists_dir")

	ls := NewLsTool().(*LS)
	granted, _ := ls.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"path": nonExistent})
	assert.True(t, granted, "沙箱启用时对不存在的目录应放行")
}

// TestLs_Sandbox_Outside_AsksUser 验证沙箱启用时越界目录 Grant 触发授权。
func TestLs_Sandbox_Outside_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outsideDir, "sub"), 0755))

	ls := NewLsTool().(*LS)
	granted, _ := ls.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"path": outsideDir})
	assert.False(t, granted, "沙箱启用时越界目录应触发授权")
}

// TestLs_Sandbox_SensitiveDir_Denies 验证沙箱启用时敏感目录 Grant 拒绝。
func TestLs_Sandbox_SensitiveDir_Denies(t *testing.T) {
	projectDir := t.TempDir()
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))

	ls := NewLsTool().(*LS)
	granted, reason := ls.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"path": sshDir})
	assert.False(t, granted, "沙箱启用时敏感目录应被拒绝")
	assert.Contains(t, reason, "敏感")
}

// TestLs_Sandbox_Execute_EnforceSensitiveDir 验证 Execute 阶段拒绝敏感目录。
func TestLs_Sandbox_Execute_EnforceSensitiveDir(t *testing.T) {
	projectDir := t.TempDir()
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))

	ls := NewLsTool().(*LS)
	_, err := ls.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{"path": sshDir})
	require.Error(t, err, "沙箱启用时 Execute 阶段应拒绝敏感目录")
}

// ----- Glob 工具沙箱集成测试 -----

// TestGlob_Sandbox_Outside_Denies 验证沙箱启用时 Glob 越界直接 Deny（不弹窗）。
// Glob 不实现 PermissionRequired，越界直接返回 error。
func TestGlob_Sandbox_Outside_Denies(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.MkdirAll(outsideDir, 0755))

	glob := NewGlobTool()
	_, err := glob.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"pattern": "*.go",
		"path":    outsideDir,
	})
	require.Error(t, err, "沙箱启用时 Glob 越界应直接拒绝")
	assert.Contains(t, err.Error(), "工作区")
}

// TestGlob_Sandbox_InWorkspace_Allows 验证沙箱启用时工作区内 Glob 放行。
func TestGlob_Sandbox_InWorkspace_Allows(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "a.go"), []byte("x"), 0644))

	glob := NewGlobTool()
	result, err := glob.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"pattern": "*.go",
		"path":    projectDir,
	})
	require.NoError(t, err, "沙箱启用时工作区内 Glob 应放行")
	m := result.(map[string]any)
	assert.Equal(t, true, m["success"])
}

// TestGlob_Sandbox_SensitiveDir_Denies 验证沙箱启用时 Glob 命中敏感目录直接拒绝。
func TestGlob_Sandbox_SensitiveDir_Denies(t *testing.T) {
	projectDir := t.TempDir()
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"), []byte("x"), 0644))

	glob := NewGlobTool()
	_, err := glob.Execute(newSandboxCtx(t, projectDir, nil), map[string]any{
		"pattern": "*",
		"path":    sshDir,
	})
	require.Error(t, err, "沙箱启用时 Glob 命中敏感目录应拒绝")
}

// ----- RunScript 工具沙箱集成测试 -----

// TestRunScript_Sandbox_NonExistent_Allows 验证沙箱启用时对不存在的脚本 Grant 放行。
func TestRunScript_Sandbox_NonExistent_Allows(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists.py")

	rs := NewRunScriptTool()
	granted, _ := rs.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "python " + nonExistent,
	})
	assert.True(t, granted, "沙箱启用时对不存在的脚本应放行")
}

// TestRunScript_Sandbox_Outside_AsksUser 验证沙箱启用时越界脚本 Grant 触发授权。
func TestRunScript_Sandbox_Outside_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideScript := filepath.Join(outsideDir, "script.py")
	require.NoError(t, os.WriteFile(outsideScript, []byte("print('hi')"), 0644))

	rs := NewRunScriptTool()
	granted, _ := rs.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "python " + outsideScript,
	})
	assert.False(t, granted, "沙箱启用时越界脚本应触发授权")
}

// TestRunScript_Sandbox_SensitiveScript_Denies 验证沙箱启用时敏感目录脚本 Grant 拒绝。
func TestRunScript_Sandbox_SensitiveScript_Denies(t *testing.T) {
	projectDir := t.TempDir()
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))
	scriptInSensitive := filepath.Join(sshDir, "helper.py")
	require.NoError(t, os.WriteFile(scriptInSensitive, []byte("print('hi')"), 0644))

	rs := NewRunScriptTool()
	granted, reason := rs.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{
		"command": "python " + scriptInSensitive,
	})
	assert.False(t, granted, "沙箱启用时敏感目录脚本应被拒绝")
	assert.Contains(t, reason, "敏感")
}

// ----- 沙箱未启用时回退旧逻辑（向后兼容性回归测试）-----

// TestSandbox_Disabled_AllTools_Fallback 验证沙箱未启用时所有工具回退到旧逻辑，
// 行为与现有 TestRead_Grant / TestLS_Grant 等保持等价。
func TestSandbox_Disabled_AllTools_Fallback(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "outside.txt"), []byte("hi"), 0644))

	ctx := newGrantCtx(t, projectDir) // 不注入沙箱

	t.Run("Read 越界触发授权（旧逻辑）", func(t *testing.T) {
		read := NewReadTool()
		granted, reason := read.Grant(ctx, map[string]any{"filePath": filepath.Join(outsideDir, "outside.txt")})
		assert.False(t, granted)
		assert.Contains(t, reason, "工作区之外")
	})

	t.Run("Ls 越界触发授权（旧逻辑）", func(t *testing.T) {
		ls := NewLsTool().(*LS)
		granted, reason := ls.Grant(ctx, map[string]any{"path": outsideDir})
		assert.False(t, granted)
		assert.Contains(t, reason, "工作区之外")
	})

	t.Run("Write 越界触发授权（旧逻辑）", func(t *testing.T) {
		write := NewWriteTool()
		granted, reason := write.Grant(ctx, map[string]any{
			"filePath": filepath.Join(outsideDir, "outside.txt"),
			"content":  "x",
		})
		assert.False(t, granted)
		assert.True(t, strings.Contains(reason, "工作区之外") || strings.Contains(reason, "工作区"),
			"reason=%q", reason)
	})

	t.Run("Edit 越界触发授权（旧逻辑）", func(t *testing.T) {
		edit := NewEditTool()
		granted, reason := edit.Grant(ctx, map[string]any{
			"file_path":   filepath.Join(outsideDir, "outside.txt"),
			"old_string": "hi",
			"new_string": "hello",
		})
		assert.False(t, granted)
		assert.Contains(t, reason, "工作区之外")
	})
}

// ----- 设备文件黑名单集成测试（沙箱启用 vs 未启用行为对齐）-----

// TestSandbox_DeviceFile_Read_Denies 验证沙箱启用时 Read 设备文件被拒绝。
// /dev/null 在大多数 Unix 上存在，读取会无限返回 0 字节。
func TestSandbox_DeviceFile_Read_Denies(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("当前系统无 /dev/null，跳过设备文件测试")
	}
	projectDir := t.TempDir()

	// 沙箱启用：Grant 阶段就拒绝（DecisionDeny）
	read := NewReadTool()
	granted, reason := read.Grant(newSandboxCtx(t, projectDir, nil), map[string]any{"filePath": "/dev/null"})
	assert.False(t, granted, "沙箱启用时 /dev/null 应被拒绝")
	assert.Contains(t, reason, "设备文件")
}

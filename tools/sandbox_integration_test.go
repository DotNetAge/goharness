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
//  8. 沙箱未注入 - 所有工具拒绝执行（安全决策统一收口到沙箱）
//  9. Glob 工具沙箱启用 - 越界直接 Deny（Execute 返回 error）
// 10. agent-browser 沙箱拦截 - 不在白名单触发授权 vs 已授权进会话白名单应放行
//
// 工具覆盖：Read / Edit / Write / Ls / Glob / RunScript / Bash。

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

// TestRead_Sandbox_Disabled_RefusesExecution 验证沙箱未注入时拒绝执行。
// 安全决策统一收口到沙箱：Grant 放行（授权无意义），Execute 拒绝执行。
func TestRead_Sandbox_Disabled_RefusesExecution(t *testing.T) {
	projectDir := t.TempDir()
	inWorkspace := filepath.Join(projectDir, "a.txt")
	require.NoError(t, os.WriteFile(inWorkspace, []byte("hi"), 0644))

	// newGrantCtx 不注入沙箱，等价于沙箱未注入
	read := NewReadTool()
	granted, _ := read.Grant(newGrantCtx(t, projectDir), map[string]any{"filePath": inWorkspace})
	assert.True(t, granted, "沙箱未注入时 Grant 放行（授权无意义）")

	_, err := read.Execute(newGrantCtx(t, projectDir), map[string]any{"filePath": inWorkspace})
	require.Error(t, err, "沙箱未注入时应拒绝执行")
	assert.Contains(t, err.Error(), "未注入沙箱")
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

// ----- 沙箱未注入时拒绝执行（安全决策统一收口到沙箱）-----

// TestSandbox_Disabled_AllTools_RefuseExecution 验证沙箱未注入时所有工具拒绝执行：
// Grant 放行（授权无意义），Execute 一律拒绝并返回引导式错误。
func TestSandbox_Disabled_AllTools_RefuseExecution(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "a.txt"), []byte("hi"), 0644))

	ctx := newGrantCtx(t, projectDir) // 不注入沙箱

	t.Run("Read 拒绝执行", func(t *testing.T) {
		read := NewReadTool()
		granted, _ := read.Grant(ctx, map[string]any{"filePath": filepath.Join(projectDir, "a.txt")})
		assert.True(t, granted, "Grant 放行（授权无意义）")
		_, err := read.Execute(ctx, map[string]any{"filePath": filepath.Join(projectDir, "a.txt")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "未注入沙箱")
	})

	t.Run("Ls 拒绝执行", func(t *testing.T) {
		ls := NewLsTool().(*LS)
		granted, _ := ls.Grant(ctx, map[string]any{"path": projectDir})
		assert.True(t, granted, "Grant 放行（授权无意义）")
		_, err := ls.Execute(ctx, map[string]any{"path": projectDir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "未注入沙箱")
	})

	t.Run("Write 拒绝执行", func(t *testing.T) {
		write := NewWriteTool()
		params := map[string]any{
			"filePath": filepath.Join(projectDir, "b.txt"),
			"content":  "x",
		}
		granted, _ := write.Grant(ctx, params)
		assert.True(t, granted, "Grant 放行（授权无意义）")
		_, err := write.Execute(ctx, params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "未注入沙箱")
	})

	t.Run("Edit 拒绝执行", func(t *testing.T) {
		edit := NewEditTool()
		params := map[string]any{
			"file_path":   filepath.Join(projectDir, "a.txt"),
			"old_string": "hi",
			"new_string": "hello",
		}
		granted, _ := edit.Grant(ctx, params)
		assert.True(t, granted, "Grant 放行（授权无意义）")
		_, err := edit.Execute(ctx, params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "未注入沙箱")
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

// ----- agent-browser 沙箱拦截测试 -----
//
// agent-browser 是经 Bash 工具调用的 CLI（SKILL.md 的 allowed-tools 声明
// Bash(agent-browser:*) 与 Bash(npx agent-browser:*) 两种形式），其命令名
// 不在 sandbox.DefaultAllowedCommands() 默认白名单中（mindx app.go 采用该默认值）。
// 以下测试对齐 mindx 真实策略，覆盖两种情况：
//   1. agent-browser 不在白名单 → Grant 触发授权（拦截），Execute 纵深拦截
//   2. 用户已授权 agent-browser 进入会话白名单 → 应放行（不再拦截）

// newCommandSandboxCtx 构造对齐 mindx 真实策略的沙箱会话上下文：
// 命令白名单 = DefaultAllowedCommands、网络拒绝网段 = DefaultDeniedSubnets
// （与 mindx/internal/core/app.go 的 sandboxPolicy 构造一致）。
func newCommandSandboxCtx(t *testing.T, projectDir string) context.Context {
	t.Helper()
	p := sandbox.SandboxPolicy{
		AllowedDirs:           []string{projectDir},
		DeniedFileGlobs:       sandbox.DefaultDeniedFileGlobs(),
		DeniedDirGlobs:        sandbox.DefaultDeniedDirGlobs(),
		DeniedDevicePaths:     sandbox.DefaultDeniedDevicePaths(),
		NetworkDenySubnets:    sandbox.DefaultDeniedSubnets(),
		AllowedCommands:       sandbox.DefaultAllowedCommands(),
		DeniedCommandPatterns: sandbox.DefaultDeniedCommandPatterns(),
		NetworkCommands:       sandbox.DefaultNetworkCommands(),
	}
	return newSandboxCtx(t, projectDir, &p)
}

// isBlockedBySandbox 判断 Bash Execute 结果是否为沙箱拦截。
// 拦截特征：exit_code=126 且 stderr 以"已阻止"开头（见 bash.go enforceCommand）。
func isBlockedBySandbox(result map[string]any) bool {
	code, _ := result["exit_code"].(int)
	stderr, _ := result["stderr"].(string)
	return code == 126 && strings.Contains(stderr, "已阻止")
}

// TestBash_AgentBrowser_NotWhitelisted_GrantAsks 场景二：不在白名单 → Grant 触发授权。
func TestBash_AgentBrowser_NotWhitelisted_GrantAsks(t *testing.T) {
	projectDir := t.TempDir()

	bash := NewBashTool().(*BashTool)
	granted, reason := bash.Grant(newCommandSandboxCtx(t, projectDir), map[string]any{
		"command": "agent-browser skills get core",
	})
	assert.False(t, granted, "agent-browser 不在命令白名单时应触发授权（granted=false）")
	assert.Contains(t, reason, "白名单")
}

// TestBash_AgentBrowser_NotWhitelisted_ExecuteBlocks 场景二：不在白名单 → Execute 纵深拦截。
// 绕过 Grant（如运行时 bug）时，Execute 阶段也不应真正执行白名单外的命令。
func TestBash_AgentBrowser_NotWhitelisted_ExecuteBlocks(t *testing.T) {
	projectDir := t.TempDir()

	bash := NewBashTool().(*BashTool)
	result, err := bash.Execute(newCommandSandboxCtx(t, projectDir), map[string]any{
		"command": "agent-browser skills get core",
		"timeout": float64(10000),
	})
	require.NoError(t, err)
	m := result.(map[string]any)
	assert.True(t, isBlockedBySandbox(m), "agent-browser 不在白名单时 Execute 应拦截（exit_code=126），实际结果=%v", m)
}

// TestBash_AgentBrowser_SessionWhitelisted_GrantAllows 场景一：已授权进会话白名单 → Grant 放行。
// 模拟用户此前对 agent-browser 点击"允许并记住本会话"（PermissionAllowSession → AddToWhitelist）。
func TestBash_AgentBrowser_SessionWhitelisted_GrantAllows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newCommandSandboxCtx(t, projectDir)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("bash", "agent-browser"))

	bash := NewBashTool().(*BashTool)
	granted, reason := bash.Grant(ctx, map[string]any{
		"command": "agent-browser skills get core",
	})
	assert.True(t, granted, "会话白名单已授权 agent-browser，Grant 应放行，实际原因=%s", reason)
}

// TestBash_AgentBrowser_SessionWhitelisted_ExecuteRuns 场景一：已授权 → Execute 不应拦截。
// 授权链路（agents/permission.go 的 executePendingAndAppend）在用户授权后直接调用 Execute；
// 若 Execute 阶段不透传会话白名单，则"记住本次会话"的授权形同虚设——
// Grant 放行了命令，Execute 却在强制检查时把 AskUser 当 Deny 拦截。
func TestBash_AgentBrowser_SessionWhitelisted_ExecuteRuns(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newCommandSandboxCtx(t, projectDir)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("bash", "agent-browser"))

	bash := NewBashTool().(*BashTool)
	result, err := bash.Execute(ctx, map[string]any{
		"command": "agent-browser --version",
		"timeout": float64(10000),
	})
	require.NoError(t, err)
	m := result.(map[string]any)
	assert.False(t, isBlockedBySandbox(m),
		"会话白名单已授权 agent-browser，Execute 仍被沙箱拦截：stderr=%s", m["stderr"])
}

// TestBash_AgentBrowser_SessionWhitelisted_LocalhostURL_Allows 验证回环 URL 默认放行：
// agent-browser 的典型用法是连接本机 CDP 端口（如 open http://127.0.0.1:9222），
// 回环地址不在默认禁止网段中，访问本机是正常行为，不应拦截。
func TestBash_AgentBrowser_SessionWhitelisted_LocalhostURL_Allows(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newCommandSandboxCtx(t, projectDir)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("bash", "agent-browser"))

	bash := NewBashTool().(*BashTool)
	granted, reason := bash.Grant(ctx, map[string]any{
		"command": "agent-browser open http://127.0.0.1:9222",
	})
	assert.True(t, granted, "回环 URL 应默认放行（访问本机是正常行为），实际原因=%s", reason)
}

// TestBash_AgentBrowser_IntranetURL_AuthorizationFlow 验证 agent-browser 访问内网 URL 的
// 授权闭环：未授权时 Grant 拒绝（弹窗），host 入会话网络白名单后 Grant 放行。
func TestBash_AgentBrowser_IntranetURL_AuthorizationFlow(t *testing.T) {
	projectDir := t.TempDir()
	ctx := newCommandSandboxCtx(t, projectDir)
	tc := GetToolContext(ctx)
	require.NoError(t, tc.Session.AddToWhitelist("bash", "agent-browser"))

	bash := NewBashTool().(*BashTool)

	// 命令已授权但目标 host 未授权：仍触发授权（原因含"网段"）
	granted, reason := bash.Grant(ctx, map[string]any{
		"command": "agent-browser open http://192.168.1.1/",
	})
	assert.False(t, granted, "命令已授权但目标 host 未授权时仍应触发授权")
	assert.Contains(t, reason, "网段")

	// 模拟用户 AllowSession：目标 host 入会话网络白名单
	require.NoError(t, tc.Session.AddToWhitelist("network", "192.168.1.1"))

	// 授权后：放行
	granted, _ = bash.Grant(ctx, map[string]any{
		"command": "agent-browser open http://192.168.1.1/",
	})
	assert.True(t, granted, "目标 host 授权后应放行")
}

// TestBash_NpxAgentBrowser_GrantAllows 记录 npx 前缀形式的当前行为：
// SKILL.md 的 allowed-tools 同时声明了 npx agent-browser 形式，其基础命令 npx
// 在默认命令白名单中 → 完全不拦截，与直接调用 agent-browser 的拦截行为不对称。
func TestBash_NpxAgentBrowser_GrantAllows(t *testing.T) {
	projectDir := t.TempDir()

	bash := NewBashTool().(*BashTool)
	granted, _ := bash.Grant(newCommandSandboxCtx(t, projectDir), map[string]any{
		"command": "npx agent-browser skills get core",
	})
	assert.True(t, granted, "npx 前缀的基础命令在默认命令白名单内，Grant 放行（与直接调用 agent-browser 的行为不对称）")
}

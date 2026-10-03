package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== CheckFile 测试 =====

// TestCheckFile_NonExistent_Allows 是核心修复场景：
// 对不存在的文件应放行，让 Execute 走 ENOENT 兜底报错，不弹授权窗。
func TestCheckFile_NonExistent_Allows(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists.txt")

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(nonExistent, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision, "不存在的文件应放行")
}

// TestCheckFile_ENOTDIR_Allows 是上次踩坑的边界场景：
// 路径中间段是文件（ENOTDIR），os.Stat 返回非 IsNotExist 错误，但文件不可能存在。
// 旧代码用 os.IsNotExist 判断会漏判，新代码用 statErr != nil 覆盖。
func TestCheckFile_ENOTDIR_Allows(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	// 创建真实文件作为路径中间段
	realFile := filepath.Join(outsideDir, "realfile.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("hi"), 0644))

	// 关键路径：/path/to/realfile.txt/sub/file.txt
	enotdirPath := filepath.Join(realFile, "sub", "file.txt")

	// 前提断言：ENOTDIR 不命中 IsNotExist
	_, statErr := os.Stat(enotdirPath)
	require.Error(t, statErr)
	require.False(t, os.IsNotExist(statErr), "ENOTDIR 不应命中 IsNotExist")

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(enotdirPath, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision, "ENOTDIR 路径文件不可能存在，应放行")
}

// TestCheckFile_SensitiveFile_Denies 验证敏感文件被硬性拒绝。
func TestCheckFile_SensitiveFile_Denies(t *testing.T) {
	projectDir := t.TempDir()
	// 在项目内创建 .env 文件
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(envFile, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision)
	assert.Contains(t, dec.Reason, "敏感文件")
}

// TestCheckFile_OutsideWorkspace_AsksUser 验证越界文件触发授权询问。
func TestCheckFile_OutsideWorkspace_AsksUser(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(outsideFile, projectDir)
	assert.Equal(t, DecisionAskUser, dec.Decision)
	assert.Contains(t, dec.Reason, "工作区")
}

// TestCheckFile_InsideWorkspace_Allows 验证工作区内非敏感文件放行。
func TestCheckFile_InsideWorkspace_Allows(t *testing.T) {
	projectDir := t.TempDir()
	normalFile := filepath.Join(projectDir, "main.go")
	require.NoError(t, os.WriteFile(normalFile, []byte("package main"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(normalFile, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision)
}

// TestCheckFile_DeniedPath_Denies 验证精确路径黑名单。
func TestCheckFile_DeniedPath_Denies(t *testing.T) {
	projectDir := t.TempDir()
	sensitiveFile := filepath.Join(projectDir, "custom_secret.txt")
	require.NoError(t, os.WriteFile(sensitiveFile, []byte("secret"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:     []string{projectDir},
		DeniedFilePaths: []string{sensitiveFile},
	})

	dec := sb.CheckFile(sensitiveFile, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision)
}

// ===== 文件白名单（宿主程序豁免）测试 =====
//
// 设计原则：白名单仅旁路"敏感文件"三类硬拒绝，
// 目录边界与设备文件黑名单不豁免（解"危险"不解"越界"）。

// TestCheckFile_AllowedFilePath_BypassesSensitiveDeny 验证精确路径白名单
// 可豁免被 DeniedFileGlobs 命中的文件（典型：.env.example 被误伤场景）。
func TestCheckFile_AllowedFilePath_BypassesSensitiveDeny(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	// 基线：无白名单时 .env 被硬拒
	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:     []string{projectDir},
		DeniedFileGlobs: DefaultDeniedFileGlobs(),
	})
	assert.Equal(t, DecisionDeny, sb.CheckFile(envFile, projectDir).Decision)

	// 加白名单后放行
	sb = newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{envFile},
	})
	dec := sb.CheckFile(envFile, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision, "白名单精确路径应豁免敏感拒绝")
}

// TestCheckFile_AllowedFileGlob_Bypasses 验证 glob 白名单按 basename 通配豁免，
// 且不影响同模式其它文件的拒绝。
func TestCheckFile_AllowedFileGlob_Bypasses(t *testing.T) {
	projectDir := t.TempDir()
	example := filepath.Join(projectDir, "server.pem.example")
	require.NoError(t, os.WriteFile(example, []byte("fake"), 0644))
	real := filepath.Join(projectDir, "server.pem")
	require.NoError(t, os.WriteFile(real, []byte("REAL"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  []string{"*.pem"},
		AllowedFileGlobs: []string{"*.pem.example"},
	})

	assert.Equal(t, DecisionAllow, sb.CheckFile(example, projectDir).Decision,
		"*.pem.example 应被 glob 白名单豁免")
	assert.Equal(t, DecisionDeny, sb.CheckFile(real, projectDir).Decision,
		"真实 .pem 仍应被拒绝")
}

// TestCheckFile_AllowedFile_StillOutsideWorkspace 验证白名单不豁免目录边界：
// 白名单文件在工作区外时仍触发 AskUser（解"危险"不解"越界"）。
func TestCheckFile_AllowedFile_StillOutsideWorkspace(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideEnv := filepath.Join(outsideDir, ".env")
	require.NoError(t, os.WriteFile(outsideEnv, []byte("X=1"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{outsideEnv},
	})

	dec := sb.CheckFile(outsideEnv, projectDir)
	assert.Equal(t, DecisionAskUser, dec.Decision,
		"白名单不应豁免目录边界，越界仍需用户授权")
}

// TestCheckFile_AllowedFile_DevicePathStillDenies 验证设备文件黑名单
// 优先于白名单（功能保护不可豁免，防进程挂起）。
func TestCheckFile_AllowedFile_DevicePathStillDenies(t *testing.T) {
	projectDir := t.TempDir()

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:       []string{projectDir},
		AllowedFilePaths:  []string{"/dev/zero"},
		DeniedDevicePaths: DefaultDeniedDevicePaths(),
	})

	dec := sb.CheckFile("/dev/zero", projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision, "设备文件不受白名单豁免")
}

// TestCheckFile_AllowedFile_InDeniedDir_Bypasses 验证白名单可豁免敏感目录段
// （如 .config 下被宿主声明为可读的特定文件）。
func TestCheckFile_AllowedFile_InDeniedDir_Bypasses(t *testing.T) {
	projectDir := t.TempDir()
	deniedDirFile := filepath.Join(projectDir, ".config", "app", "settings.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(deniedDirFile), 0755))
	require.NoError(t, os.WriteFile(deniedDirFile, []byte("k: v"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedDirGlobs:   DefaultDeniedDirGlobs(),
		AllowedFilePaths: []string{deniedDirFile},
	})

	dec := sb.CheckFile(deniedDirFile, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision, "白名单应豁免敏感目录段拒绝")
}

// TestEnforceFile_AllowedFilePath 验证 Execute 阶段同样感知白名单。
func TestEnforceFile_AllowedFilePath(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{envFile},
	})

	assert.NoError(t, sb.EnforceFile(envFile, projectDir),
		"Execute 阶段白名单应同样豁免")
}

// TestCheckFile_AllowedFilePath_Glob_Subtree 验证 allowed_paths 路径通配：
// 含 "*" 的条目按完整路径模式匹配（单个 * 跨任意层级），放行整个子树的
// 敏感检查；模式锚定（前缀相似的目录不命中）；不含通配符的条目行为不变。
func TestCheckFile_AllowedFilePath_Glob_Subtree(t *testing.T) {
	projectDir := t.TempDir()
	// 用默认敏感名单中的精确文件名 id_rsa(单文件名命中 DeniedFileGlobs)
	nested := filepath.Join(projectDir, "sub", "id_rsa")
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0755))
	require.NoError(t, os.WriteFile(nested, []byte("KEY"), 0644))
	// 前缀相似但不应命中的兄弟目录(同名敏感文件,仅路径前缀不同)
	sibling := projectDir + "-sibling"
	siblingFile := filepath.Join(sibling, "id_rsa")
	require.NoError(t, os.MkdirAll(sibling, 0755))
	require.NoError(t, os.WriteFile(siblingFile, []byte("KEY"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{projectDir + "/*"},
	})

	assert.Equal(t, DecisionAllow, sb.CheckFile(nested, projectDir).Decision,
		"通配条目应放行子树内多层敏感文件")
	assert.Equal(t, DecisionDeny, sb.CheckFile(siblingFile, projectDir).Decision,
		"前缀相似的兄弟目录不应被通配命中")
}

// TestCheckFile_AllowedFilePath_Glob_StillOutsideWorkspace 验证通配命中
// 同样不豁免目录边界（解"危险"不解"越界"，与精确路径语义一致）。
func TestCheckFile_AllowedFilePath_Glob_StillOutsideWorkspace(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideEnv := filepath.Join(outsideDir, ".env")
	require.NoError(t, os.WriteFile(outsideEnv, []byte("X=1"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:      []string{projectDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{outsideDir + "/*"},
	})

	dec := sb.CheckFile(outsideEnv, projectDir)
	assert.Equal(t, DecisionAskUser, dec.Decision,
		"通配白名单不应豁免目录边界，越界仍需用户授权")
}

// TestEnforceFile_AllowedFilePath_Glob_SymlinkPrefix 验证 Enforce 阶段
// 通配模式的符号链接前缀归一化：白名单用符号链接路径书写（如 macOS 上
// /tmp → /private/tmp），访问真实路径文件时模式前缀同步解析后仍应命中。
func TestEnforceFile_AllowedFilePath_Glob_SymlinkPrefix(t *testing.T) {
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, linkDir))
	envFile := filepath.Join(realDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		// realDir 设为允许目录：越界检查不拦，让敏感豁免成为唯一验证变量
		AllowedDirs:      []string{realDir},
		DeniedFileGlobs:  DefaultDeniedFileGlobs(),
		AllowedFilePaths: []string{linkDir + "/*"},
	})

	// 访问路径为真实目录（realDir），白名单模式用符号链接路径（linkDir/*）：
	// 前缀归一化后两侧同基准，应命中豁免（.env 无白名单时会被硬拒）
	assert.NoError(t, sb.EnforceFile(envFile, realDir),
		"通配模式前缀应随符号链接归一化后命中")
}

// TestCheckFile_GlobMatch 验证 glob 模式匹配各类敏感文件名。
func TestCheckFile_GlobMatch(t *testing.T) {
	projectDir := t.TempDir()
	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    []string{".env*", "*.pem", "credentials*"},
	})

	cases := []struct {
		name     string
		filename string
		want     Decision
	}{
		{".env", ".env", DecisionDeny},
		{".env.local", ".env.local", DecisionDeny},
		{".env.production", ".env.production", DecisionDeny},
		{"server.pem", "server.pem", DecisionDeny},
		{"key.PEM", "key.PEM", DecisionDeny}, // 大小写不敏感
		{"credentials.json", "credentials.json", DecisionDeny},
		{"main.go", "main.go", DecisionAllow},
		{"env.txt", "env.txt", DecisionAllow}, // 不匹配 .env*（无前导点）
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(projectDir, c.filename)
			require.NoError(t, os.WriteFile(path, []byte("x"), 0644))
			dec := sb.CheckFile(path, projectDir)
			assert.Equal(t, c.want, dec.Decision)
		})
	}
}

// TestCheckFile_NoAllowedDirs_FallbackToProjectDir 验证：
// AllowedDirs 为空时回退到 projectDir 做边界检查（向后兼容）。
func TestCheckFile_NoAllowedDirs_FallbackToProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		// AllowedDirs 为空
		DeniedFileGlobs: DefaultDeniedFileGlobs(),
	})

	// 越界文件应触发 AskUser
	dec := sb.CheckFile(outsideFile, projectDir)
	assert.Equal(t, DecisionAskUser, dec.Decision)
}

// TestCheckFile_NoAllowedDirs_NoProjectDir_Allows 验证：
// 既无 AllowedDirs 也无 projectDir 时放行（向后兼容旧行为）。
func TestCheckFile_NoAllowedDirs_NoProjectDir_Allows(t *testing.T) {
	projectDir := t.TempDir()
	outsideFile := filepath.Join(projectDir, "any.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		// AllowedDirs 为空
		DeniedFileGlobs: DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFile(outsideFile, "")
	assert.Equal(t, DecisionAllow, dec.Decision)
}

// ===== EnforceFile 测试 =====

// TestEnforceFile_SymlinkBypassBlocked 验证符号链接绕过被阻止：
// Grant 阶段放行（项目内文件），但攻击者在 Grant 后把文件替换为指向 /etc/passwd 的符号链接。
// EnforceFile 在 Execute 阶段解析符号链接后重新检查，应拒绝。
func TestEnforceFile_SymlinkBypassBlocked(t *testing.T) {
	projectDir := t.TempDir()
	targetFile := filepath.Join(projectDir, "link.txt")

	// 先创建一个合法文件让 Grant 通过
	require.NoError(t, os.WriteFile(targetFile, []byte("safe"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFilePaths:    []string{"/etc/passwd"},
	})

	// 模拟 Grant 放行
	dec := sb.CheckFile(targetFile, projectDir)
	require.Equal(t, DecisionAllow, dec.Decision)

	// 攻击者替换为符号链接指向 /etc/passwd
	require.NoError(t, os.Remove(targetFile))
	require.NoError(t, os.Symlink("/etc/passwd", targetFile))

	// EnforceFile 应基于真实路径拒绝
	err := sb.EnforceFile(targetFile, projectDir)
	assert.Error(t, err)
}

// TestEnforceFile_NonExistent_NoError 验证不存在的文件不报错（让 Execute 兜底）。
func TestEnforceFile_NonExistent_NoError(t *testing.T) {
	projectDir := t.TempDir()
	nonExistent := filepath.Join(projectDir, "never_exists.txt")

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
	})

	err := sb.EnforceFile(nonExistent, projectDir)
	assert.NoError(t, err, "不存在的文件应不报错，让 Execute 兜底")
}

// TestEnforceFile_OutsideWorkspace_Error 验证越界文件报错。
func TestEnforceFile_OutsideWorkspace_Error(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
	})

	err := sb.EnforceFile(outsideFile, projectDir)
	assert.Error(t, err)
}

// ===== 新增字段测试 =====

// TestCheckFile_DevicePath_Denies 验证设备文件被拒绝。
// 修复"问题 3"：原沙箱未覆盖设备文件黑名单。
func TestCheckFile_DevicePath_Denies(t *testing.T) {
	projectDir := t.TempDir()

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedDevicePaths:  DefaultDeniedDevicePaths(),
	})

	// /dev/null 在大多数 Unix 系统上存在
	if _, err := os.Stat("/dev/null"); err == nil {
		dec := sb.CheckFile("/dev/null", projectDir)
		assert.Equal(t, DecisionDeny, dec.Decision, "设备文件应被拒绝")
		assert.Contains(t, dec.Reason, "设备文件")
	}
}

// TestCheckFile_DeniedDir_Denies 验证敏感目录段命中被拒绝。
// 修复"问题 1"：原沙箱未实现 DeniedDirGlobs。
func TestCheckFile_DeniedDir_Denies(t *testing.T) {
	projectDir := t.TempDir()
	// 模拟 ~/.ssh/config 路径
	sshDir := filepath.Join(projectDir, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0755))
	sshConfig := filepath.Join(sshDir, "config")
	require.NoError(t, os.WriteFile(sshConfig, []byte("Host *"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedDirGlobs:     DefaultDeniedDirGlobs(),
	})

	dec := sb.CheckFile(sshConfig, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision, "路径包含 .ssh 段应被拒绝")
}

// TestCheckFile_DeniedDir_NoMatch_Allows 验证非敏感目录放行。
func TestCheckFile_DeniedDir_NoMatch_Allows(t *testing.T) {
	projectDir := t.TempDir()
	normalFile := filepath.Join(projectDir, "src", "main.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(normalFile), 0755))
	require.NoError(t, os.WriteFile(normalFile, []byte("package main"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedDirGlobs:     DefaultDeniedDirGlobs(),
	})

	dec := sb.CheckFile(normalFile, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision)
}

// TestEnforceFile_DevicePath_Error 验证 EnforceFile 也拦截设备文件。
func TestEnforceFile_DevicePath_Error(t *testing.T) {
	projectDir := t.TempDir()

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedDevicePaths:  DefaultDeniedDevicePaths(),
	})

	if _, err := os.Stat("/dev/null"); err == nil {
		err := sb.EnforceFile("/dev/null", projectDir)
		assert.Error(t, err)
	}
}

// TestEnforceFile_DeniedDir_Error 验证 EnforceFile 也拦截敏感目录段。
func TestEnforceFile_DeniedDir_Error(t *testing.T) {
	projectDir := t.TempDir()
	awsDir := filepath.Join(projectDir, ".aws")
	require.NoError(t, os.MkdirAll(awsDir, 0755))
	credsFile := filepath.Join(awsDir, "credentials")
	require.NoError(t, os.WriteFile(credsFile, []byte("[default]"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedDirGlobs:     DefaultDeniedDirGlobs(),
	})

	err := sb.EnforceFile(credsFile, projectDir)
	assert.Error(t, err)
}

// TestIsDevicePath 验证设备路径匹配（精确匹配，大小写敏感）。
func TestIsDevicePath(t *testing.T) {
	sb := newTestSandbox(t, &SandboxPolicy{
		DeniedDevicePaths: DefaultDeniedDevicePaths(),
	})
	p := sb.Policy()

	assert.True(t, sb.isDevicePath("/dev/zero", &p))
	assert.True(t, sb.isDevicePath("/dev/random", &p))
	assert.True(t, sb.isDevicePath("/proc/self/fd/0", &p))
	assert.False(t, sb.isDevicePath("/dev/Zero", &p)) // 大小写敏感
	assert.False(t, sb.isDevicePath("/etc/passwd", &p))
	assert.True(t, sb.isDevicePath("/dev/./zero", &p)) // Clean 后是 /dev/zero，应匹配
}

// TestIsInDeniedDir 验证目录段匹配。
func TestIsInDeniedDir(t *testing.T) {
	sb := newTestSandbox(t, &SandboxPolicy{
		DeniedDirGlobs: DefaultDeniedDirGlobs(),
	})
	p := sb.Policy()

	cases := []struct {
		path string
		want bool
	}{
		{"/Users/ray/.ssh/config", true},
		{"/Users/ray/.aws/credentials", true},
		{"/home/user/.kube/config", true},
		{"/project/src/main.go", false},
		{"/project/.config/app/settings.toml", true}, // .config 在默认列表
		{"/project/config/app.toml", false},           // config 不带点，不匹配
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := sb.isInDeniedDir(c.path, &p)
			assert.Equal(t, c.want, got)
		})
	}
}

// ===== Glob 工具简化决策路径测试 =====

// TestCheckFileAllowOrDeny_OutsideWorkspace_Denies 是 Glob 工具的核心场景：
// 越界访问不触发 AskUser，直接 Deny。
// 修复"问题 4"：Glob 不实现 PermissionRequired，需要简化决策路径。
func TestCheckFileAllowOrDeny_OutsideWorkspace_Denies(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("hi"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
	})

	dec := sb.CheckFileAllowOrDeny(outsideFile, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision, "Glob 路径越界应直接拒绝，不弹窗")
	assert.Contains(t, dec.Reason, "工作区")
}

// TestCheckFileAllowOrDeny_InsideWorkspace_Allows 验证工作区内放行。
func TestCheckFileAllowOrDeny_InsideWorkspace_Allows(t *testing.T) {
	projectDir := t.TempDir()
	normalFile := filepath.Join(projectDir, "main.go")
	require.NoError(t, os.WriteFile(normalFile, []byte("package main"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
	})

	dec := sb.CheckFileAllowOrDeny(normalFile, projectDir)
	assert.Equal(t, DecisionAllow, dec.Decision)
}

// TestCheckFileAllowOrDeny_SensitiveFile_Denies 验证敏感文件在简化路径下也被拒绝。
func TestCheckFileAllowOrDeny_SensitiveFile_Denies(t *testing.T) {
	projectDir := t.TempDir()
	envFile := filepath.Join(projectDir, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("SECRET=xxx"), 0644))

	sb := newTestSandbox(t, &SandboxPolicy{
		AllowedDirs:        []string{projectDir},
		DeniedFileGlobs:    DefaultDeniedFileGlobs(),
	})

	dec := sb.CheckFileAllowOrDeny(envFile, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision)
}

// ===== matchGlob 单元测试 =====

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{".env", ".env", true},
		{".env", ".envrc", false},
		{".env*", ".env", true},
		{".env*", ".env.local", true},
		{".env*", ".env.production", true},
		{".env*", "env.txt", false},
		{"*.pem", "server.pem", true},
		{"*.pem", "server.crt", false},
		{"credentials*", "credentials.json", true},
		{"credentials*", "creds.json", false},
		{"*", "anything", true},
		{"*.*", "file.txt", true},
		{"exact", "exact", true},
		{"exact", "exact2", false},
	}

	for _, c := range cases {
		t.Run(c.pattern+"_"+c.name, func(t *testing.T) {
			got := matchGlob(c.pattern, c.name)
			assert.Equal(t, c.want, got)
		})
	}
}

// ===== pathWithinDir 单元测试 =====

func TestPathWithinDir(t *testing.T) {
	cases := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{"完全匹配", "/project", "/project", true},
		{"子路径", "/project/sub/file.txt", "/project", true},
		{"兄弟路径", "/projects/file.txt", "/project", false},
		{"父路径反斜", "/project/../etc/passwd", "/project", false},
		{"子路径含..", "/project/sub/../../etc/passwd", "/project", false},
		{"根目录", "/", "/", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pathWithinDir(c.path, c.dir)
			assert.Equal(t, c.want, got)
		})
	}
}

// ===== newTestSandbox 引用，避免 unused 警告 =====

func TestNewTestSandboxHelper(t *testing.T) {
	sb := newTestSandbox(t, nil)
	assert.NotNil(t, sb)
	_ = logging.NewNopLogger // 保持 logging 引用
}

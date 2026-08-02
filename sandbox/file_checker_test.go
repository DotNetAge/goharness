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
		AllowedDirs:        []string{projectDir},
		DeniedFilePaths:    []string{sensitiveFile},
	})

	dec := sb.CheckFile(sensitiveFile, projectDir)
	assert.Equal(t, DecisionDeny, dec.Decision)
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

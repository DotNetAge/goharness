package sandbox

import (
	"net"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSandbox 构造测试用沙箱，使用默认策略。
func newTestSandbox(t *testing.T, policy *SandboxPolicy) *Sandbox {
	t.Helper()
	if policy == nil {
		p := DefaultPolicy()
		policy = &p
	}
	sb, err := NewSandbox(policy, logging.NewNopLogger())
	require.NoError(t, err)
	return sb
}

// DefaultPolicy 返回组合了所有默认策略的 SandboxPolicy。
// 这是测试辅助函数，也是宿主应用快速启用的便利方法。
func DefaultPolicy() SandboxPolicy {
	return SandboxPolicy{
		DeniedFileGlobs:        DefaultDeniedFileGlobs(),
		DeniedDirGlobs:        DefaultDeniedDirGlobs(),
		DeniedDevicePaths:     DefaultDeniedDevicePaths(),
		NetworkDenySubnets:    DefaultDeniedSubnets(),
		AllowedCommands:       DefaultAllowedCommands(),
		DeniedCommandPatterns: DefaultDeniedCommandPatterns(),
		NetworkCommands:       DefaultNetworkCommands(),
	}
}

// ===== decision.go 测试 =====

func TestDecision_String(t *testing.T) {
	assert.Equal(t, "允许", DecisionAllow.String())
	assert.Equal(t, "拒绝", DecisionDeny.String())
	assert.Equal(t, "询问用户", DecisionAskUser.String())
	assert.Equal(t, "未知", Decision(99).String())
}

// ===== policy.go 测试 =====

func TestSandboxPolicy_Compile_NormalizesPaths(t *testing.T) {
	p := SandboxPolicy{
		AllowedDirs:      []string{"/project//sub/", "/project/../parent"},
		DeniedFilePaths:  []string{"/etc//passwd"},
		DeniedFileGlobs:  []string{".ENV", "*.PEM"},
		AllowedCommands:  []string{"CURL", "WGet"},
		NetworkCommands:  []string{"Curl"},
	}
	compiled, err := p.Compile()
	require.NoError(t, err)

	assert.Equal(t, []string{"/project/sub", "/parent"}, compiled.AllowedDirs)
	assert.Equal(t, []string{"/etc/passwd"}, compiled.DeniedFilePaths)
	assert.Equal(t, []string{".env", "*.pem"}, compiled.DeniedFileGlobs)
	assert.Equal(t, []string{"curl", "wget"}, compiled.AllowedCommands)
	assert.Equal(t, []string{"curl"}, compiled.NetworkCommands)
}

func TestSandboxPolicy_Compile_FiltersEmpty(t *testing.T) {
	p := SandboxPolicy{
		AllowedDirs:     []string{"", "  ", "/valid"},
		DeniedFileGlobs: []string{"", "*.pem"},
	}
	compiled, err := p.Compile()
	require.NoError(t, err)
	assert.Equal(t, []string{"/valid"}, compiled.AllowedDirs)
	assert.Equal(t, []string{"*.pem"}, compiled.DeniedFileGlobs)
}

func TestSandboxPolicy_Compile_NilSubnetsRejected(t *testing.T) {
	p := SandboxPolicy{
		NetworkDenySubnets: []*net.IPNet{nil},
	}
	_, err := p.Compile()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// ===== sandbox.go 测试 =====

func TestNewSandbox_RequiresPolicy(t *testing.T) {
	_, err := NewSandbox(nil, logging.NewNopLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy")
}

func TestNewSandbox_RequiresLogger(t *testing.T) {
	p := DefaultPolicy()
	_, err := NewSandbox(&p, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logger")
}

func TestSandbox_Policy_ReturnsSnapshot(t *testing.T) {
	p := DefaultPolicy()
	sb, err := NewSandbox(&p, logging.NewNopLogger())
	require.NoError(t, err)

	snap := sb.Policy()
	snap.AllowedDirs = []string{"/modified"}

	// 原策略不受影响（值拷贝）
	assert.NotEqual(t, []string{"/modified"}, sb.Policy().AllowedDirs)
}

func TestSandbox_UpdatePolicy_Atomic(t *testing.T) {
	p := DefaultPolicy()
	sb, err := NewSandbox(&p, logging.NewNopLogger())
	require.NoError(t, err)

	newP := SandboxPolicy{
		DeniedFileGlobs: []string{"*.key"},
	}
	require.NoError(t, sb.UpdatePolicy(&newP))

	// 新策略生效
	assert.Equal(t, []string{"*.key"}, sb.Policy().DeniedFileGlobs)
}

func TestSandbox_UpdatePolicy_RejectsNil(t *testing.T) {
	p := DefaultPolicy()
	sb, err := NewSandbox(&p, logging.NewNopLogger())
	require.NoError(t, err)

	err = sb.UpdatePolicy(nil)
	require.Error(t, err)
}

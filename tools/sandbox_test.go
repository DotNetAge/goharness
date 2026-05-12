package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultSandboxConfig(t *testing.T) {
	cfg := DefaultSandboxConfig()
	if !cfg.Enabled {
		t.Error("sandbox should be enabled by default")
	}
	if cfg.Profile != ProfileWorkspace {
		t.Errorf("expected workspace profile, got %s", cfg.Profile)
	}
	if cfg.TempDir == "" {
		t.Error("temp dir should not be empty")
	}
}

func TestUnrestrictedSandboxConfig(t *testing.T) {
	cfg := UnrestrictedSandboxConfig()
	if cfg.Enabled {
		t.Error("unrestricted sandbox should be disabled")
	}
	if cfg.Profile != ProfileUnconfined {
		t.Errorf("expected unconfined profile, got %s", cfg.Profile)
	}
}

func TestRestrictedSandboxConfig(t *testing.T) {
	cfg := RestrictedSandboxConfig("/tmp/test")
	if !cfg.Enabled {
		t.Error("restricted sandbox should be enabled")
	}
	if cfg.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", cfg.Profile)
	}
	if len(cfg.AllowedPaths) != 1 || cfg.AllowedPaths[0] != "/tmp/test" {
		t.Errorf("expected allowed path /tmp/test, got %v", cfg.AllowedPaths)
	}
	if cfg.AllowNetwork {
		t.Error("restricted sandbox should not allow network")
	}
}

func TestSandboxConfigConcurrency(t *testing.T) {
	cfg := DefaultSandboxConfig()

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			cfg.GetEnabled()
			cfg.GetProfile()
			cfg.GetAllowedPaths()
			cfg.GetAllowNetwork()
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestSandboxConfigUpdate(t *testing.T) {
	cfg := DefaultSandboxConfig()
	cfg.Update(func(c *SandboxConfig) {
		c.Enabled = false
		c.Profile = ProfileSandbox
		c.AllowNetwork = false
	})

	if cfg.Enabled {
		t.Error("sandbox should be disabled after update")
	}
	if cfg.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", cfg.Profile)
	}
	if cfg.AllowNetwork {
		t.Error("network should be disabled after update")
	}
}

func TestSessionSandboxManager(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	if mgr == nil {
		t.Fatal("failed to create session sandbox manager")
	}

	sessionID := "test-session-1"
	cfg := mgr.GetConfig(sessionID)
	if !cfg.Enabled {
		t.Error("session should use default config")
	}

	customCfg := &SandboxConfig{
		Enabled:      true,
		Profile:      ProfileSandbox,
		AllowedPaths: []string{"/tmp/test"},
		AllowNetwork: false,
	}
	mgr.SetConfig(sessionID, customCfg)

	retrievedCfg := mgr.GetConfig(sessionID)
	if retrievedCfg.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", retrievedCfg.Profile)
	}
	if len(retrievedCfg.AllowedPaths) != 1 || retrievedCfg.AllowedPaths[0] != "/tmp/test" {
		t.Errorf("expected allowed path /tmp/test, got %v", retrievedCfg.AllowedPaths)
	}
}

func TestSessionSandboxManagerUpdate(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	sessionID := "test-session-2"

	mgr.UpdateConfig(sessionID, func(cfg *SandboxConfig) {
		cfg.AllowNetwork = false
	})

	cfg := mgr.GetConfig(sessionID)
	if cfg.AllowNetwork {
		t.Error("network should be disabled after update")
	}
}

func TestSessionSandboxManagerCleanup(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	sessionID := "test-session-3"

	mgr.SetConfig(sessionID, &SandboxConfig{
		Enabled: true,
		Profile: ProfileWorkspace,
	})

	mgr.CleanupSession(sessionID)
	cfg := mgr.GetConfig(sessionID)

	if !cfg.Enabled {
		t.Error("cleanup should remove session, fallback to default")
	}

	sessions := mgr.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after cleanup, got %d", len(sessions))
	}
}

func TestSessionContextID(t *testing.T) {
	ctx := context.Background()
	sessionID := "test-session-id"

	ctxWithID := WithSessionID(ctx, sessionID)
	extractedID := ExtractSessionID(ctxWithID)

	if extractedID != sessionID {
		t.Errorf("expected session id %s, got %s", sessionID, extractedID)
	}

	extractedFromEmptyCtx := ExtractSessionID(ctx)
	if extractedFromEmptyCtx != "" {
		t.Errorf("expected empty session id, got %s", extractedFromEmptyCtx)
	}
}

func TestApplySandboxDisabled(t *testing.T) {
	cfg := &SandboxConfig{
		Enabled: false,
	}

	cmd := exec.Command("echo", "test")
	result := ApplySandbox(cmd, cfg)
	if result != cmd {
		t.Error("ApplySandbox should return original cmd when disabled")
	}
}

func TestGenerateSessionTempDir(t *testing.T) {
	sessionID := "test-session"
	tempDir := GenerateSessionTempDir(sessionID, "")

	expectedSuffix := filepath.Join("goreact-sandbox", sessionID)
	if !strings.HasSuffix(tempDir, expectedSuffix) {
		t.Errorf("expected temp dir to end with %s, got %s", expectedSuffix, tempDir)
	}
}

func TestSandboxProfileGeneration(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt only available on macOS")
	}

	cfg := &SandboxConfig{
		Enabled:      true,
		Profile:      ProfileWorkspace,
		AllowedPaths: []string{"/tmp/test-workspace"},
		AllowNetwork: true,
	}

	profile := generateSeatbeltProfile(cfg)
	if profile == "" {
		t.Fatal("profile should not be empty")
	}

	if !strings.Contains(profile, "(version 1)") {
		t.Error("profile should contain version")
	}

	if !strings.Contains(profile, "(allow file-read* file-write*") {
		t.Error("workspace profile should allow read/write to allowed paths")
	}

	if !strings.Contains(profile, "(allow network*)") {
		t.Error("profile should allow network when configured")
	}
}

func TestSeatbeltStrictProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt only available on macOS")
	}

	cfg := &SandboxConfig{
		Enabled:      true,
		Profile:      ProfileSandbox,
		AllowedPaths: []string{"/tmp/test-workspace"},
		AllowNetwork: false,
	}

	profile := generateSeatbeltProfile(cfg)

	if !strings.Contains(profile, "(deny default)") {
		t.Error("strict profile should deny by default")
	}

	if !strings.Contains(profile, "(deny network*)") {
		t.Error("strict profile should deny network")
	}
}

func TestSeatbeltCustomPolicy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt only available on macOS")
	}

	customPolicy := `(version 1)
(allow default)`

	cfg := &SandboxConfig{
		CustomPolicy: customPolicy,
	}

	profile := generateSeatbeltProfile(cfg)
	if profile != customPolicy {
		t.Error("custom policy should be used when provided")
	}
}

func TestBashToolWithSandbox(t *testing.T) {
	tool := NewBashTool()
	bashTool, ok := tool.(*BashTool)
	if !ok {
		t.Fatal("failed to create bash tool")
	}

	if !bashTool.sandboxConfig.Enabled {
		t.Error("bash tool should have sandbox enabled by default")
	}

	restrictedCfg := RestrictedSandboxConfig("/tmp/test")
	bashTool.SetSandboxConfig(restrictedCfg)

	if bashTool.sandboxConfig.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", bashTool.sandboxConfig.Profile)
	}
}

func TestBashToolWithSessionSandbox(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	tool := NewBashToolWithSessionSandbox(mgr)
	bashTool, ok := tool.(*BashTool)
	if !ok {
		t.Fatal("failed to create bash tool with session sandbox")
	}

	if bashTool.sessionSandboxMgr == nil {
		t.Error("bash tool should have session sandbox manager")
	}
}

func TestRunScriptToolWithSandbox(t *testing.T) {
	tool := NewRunScriptTool()
	runScriptTool, ok := tool.(*RunScript)
	if !ok {
		t.Fatal("failed to create run script tool")
	}

	if !runScriptTool.sandboxConfig.Enabled {
		t.Error("run script tool should have sandbox enabled by default")
	}

	restrictedCfg := RestrictedSandboxConfig("/tmp/test")
	runScriptTool.SetSandboxConfig(restrictedCfg)

	if runScriptTool.sandboxConfig.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", runScriptTool.sandboxConfig.Profile)
	}
}

func TestRunScriptToolWithSessionSandbox(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	tool := NewRunScriptToolWithSessionSandbox(mgr)
	runScriptTool, ok := tool.(*RunScript)
	if !ok {
		t.Fatal("failed to create run script tool with session sandbox")
	}

	if runScriptTool.sessionSandboxMgr == nil {
		t.Error("run script tool should have session sandbox manager")
	}
}

func TestPowerShellToolWithSandbox(t *testing.T) {
	tool := NewPowerShellTool()
	powerShellTool, ok := tool.(*PowerShellTool)
	if !ok {
		t.Fatal("failed to create powershell tool")
	}

	if !powerShellTool.sandboxConfig.Enabled {
		t.Error("powershell tool should have sandbox enabled by default")
	}

	restrictedCfg := RestrictedSandboxConfig("/tmp/test")
	powerShellTool.SetSandboxConfig(restrictedCfg)

	if powerShellTool.sandboxConfig.Profile != ProfileSandbox {
		t.Errorf("expected sandbox profile, got %s", powerShellTool.sandboxConfig.Profile)
	}
}

func TestPowerShellToolWithSessionSandbox(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")
	tool := NewPowerShellToolWithSessionSandbox(mgr)
	powerShellTool, ok := tool.(*PowerShellTool)
	if !ok {
		t.Fatal("failed to create powershell tool with session sandbox")
	}

	if powerShellTool.sessionSandboxMgr == nil {
		t.Error("powershell tool should have session sandbox manager")
	}
}

func TestGlobalSandboxApplier(t *testing.T) {
	applier := func(cmd *exec.Cmd, config *SandboxConfig) *exec.Cmd {
		return cmd
	}

	SetGlobalSandboxApplier(applier)
	retrievedApplier := GetGlobalSandboxApplier()

	if retrievedApplier == nil {
		t.Error("global sandbox applier should be set")
	}
}

func TestSeatbeltProfileWriteToTemp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt only available on macOS")
	}

	profile := `(version 1)
(allow default)`

	tempFile, err := writeProfileToTemp(profile)
	if err != nil {
		t.Fatalf("failed to write profile to temp: %v", err)
	}
	defer os.Remove(tempFile)

	content, err := os.ReadFile(tempFile)
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}

	if string(content) != profile {
		t.Error("temp file content should match profile")
	}
}

func TestEnsureTempDir(t *testing.T) {
	tempDir := filepath.Join(os.TempDir(), "goreact-test-tempdir")
	defer os.RemoveAll(tempDir)

	err := ensureTempDir(tempDir)
	if err != nil {
		t.Fatalf("failed to ensure temp dir: %v", err)
	}

	info, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("temp dir should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("temp dir should be a directory")
	}
}

func TestSessionSandboxManagerClearAll(t *testing.T) {
	mgr := NewSessionSandboxManager("/tmp/test-project", "")

	mgr.SetConfig("session-1", &SandboxConfig{Enabled: true})
	mgr.SetConfig("session-2", &SandboxConfig{Enabled: true})

	if len(mgr.ListSessions()) != 2 {
		t.Error("expected 2 sessions")
	}

	mgr.ClearAll()

	if len(mgr.ListSessions()) != 0 {
		t.Error("expected 0 sessions after clear all")
	}
}

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// SessionSandboxManager manages per-session sandbox configurations with full
// directory context awareness (Agent Native Design: 4-Layer Architecture).
//
// Key Design Principles:
//   - SESSION_DIR is the root for all session-scoped resources
//   - Each session gets isolated TempDir = ${SESSION_DIR}/tmp
//   - AllowedPaths automatically includes PROJECT_DIR and SESSION_DIR
//   - Lifecycle management ensures cleanup on session removal
//
// Integration Point:
//   This should be created once per Agent and shared across all tools.
//   The Agent is responsible for injecting it via tool constructors.
type SessionSandboxManager struct {
	mu            sync.RWMutex
	sessions      map[string]*SandboxConfig
	defaultConfig *SandboxConfig

	// Directory context (set at construction time, immutable)
	projectDir     string // Layer 2: Project working directory
	sessionBaseDir string // Layer 3 base: Parent dir for all session directories (e.g., ~/.mindx/sessions)
}

// NewSessionSandboxManager creates a manager with full directory context.
//
// Parameters:
//   - projectDir: Layer 2 directory (required, used for AllowedPaths)
//   - sessionBaseDir: Layer 3 base directory (optional, enables session isolation)
//
// When sessionBaseDir is provided:
//   - Each session gets its own directory: ${sessionBaseDir}/${sessionID}
//   - TempDir is automatically set to ${sessionDir}/tmp
//   - Both projectDir and sessionDir are in AllowedPaths
//
// When sessionBaseDir is empty:
//   - Falls back to system temp directory (backward compatibility)
//   - No session isolation (all sessions share the same sandbox)
func NewSessionSandboxManager(projectDir string, sessionBaseDir string) *SessionSandboxManager {
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	defaultCfg := NewSandboxConfigWithDirs(projectDir, "")

	if sessionBaseDir != "" {
		defaultCfg.TempDir = filepath.Join(sessionBaseDir, "default", "tmp")
	} else {
		// Backward compatibility: use system temp when no session base dir
		defaultCfg.TempDir = filepath.Join(defaultCfg.TempDir, "default")
	}

	return &SessionSandboxManager{
		sessions:       make(map[string]*SandboxConfig),
		defaultConfig:  defaultCfg,
		projectDir:     projectDir,
		sessionBaseDir: sessionBaseDir,
	}
}

// NewSessionSandboxManagerFromConfig creates a manager from an existing SandboxConfig.
// This preserves backward compatibility while adopting the new architecture.
func NewSessionSandboxManagerFromConfig(defaultConfig *SandboxConfig) *SessionSandboxManager {
	if defaultConfig == nil {
		defaultConfig = DefaultSandboxConfig()
	}

	projectDir := defaultConfig.ProjectDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	var sessionBaseDir string
	if defaultConfig.HasSessionIsolation() {
		sessionBaseDir = filepath.Dir(defaultConfig.SessionDir)
	}

	cfgCopy := &SandboxConfig{}
	*cfgCopy = *defaultConfig

	return &SessionSandboxManager{
		sessions:       make(map[string]*SandboxConfig),
		defaultConfig:  cfgCopy,
		projectDir:     projectDir,
		sessionBaseDir: sessionBaseDir,
	}
}

func (m *SessionSandboxManager) GetConfig(sessionID string) *SandboxConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if cfg, ok := m.sessions[sessionID]; ok {
		return m.copyConfig(cfg)
	}

	return m.createDefaultSessionConfig(sessionID)
}

func (m *SessionSandboxManager) SetConfig(sessionID string, config *SandboxConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfgCopy := m.prepareSessionConfig(sessionID, config)

	if config.Enabled != cfgCopy.Enabled {
		cfgCopy.Enabled = config.Enabled
	}
	if config.Profile != "" {
		cfgCopy.Profile = config.Profile
	}
	if len(config.AllowedPaths) > 0 {
		cfgCopy.AllowedPaths = config.AllowedPaths
	}
	config.AllowNetwork = cfgCopy.AllowNetwork
	if config.CustomPolicy != "" {
		cfgCopy.CustomPolicy = config.CustomPolicy
	}
	if config.TempDir != "" {
		cfgCopy.TempDir = config.TempDir
	}

	m.sessions[sessionID] = cfgCopy

	if cfgCopy.TempDir != "" {
		os.MkdirAll(cfgCopy.TempDir, 0755)
	}
}

func (m *SessionSandboxManager) UpdateConfig(sessionID string, fn func(cfg *SandboxConfig)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.sessions[sessionID]
	if !ok {
		cfg = m.createDefaultSessionConfigLocked(sessionID)
		m.sessions[sessionID] = cfg
	}

	fn(cfg)

	if cfg.TempDir != "" {
		os.MkdirAll(cfg.TempDir, 0755)
	}
}

func (m *SessionSandboxManager) RemoveSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.sessions[sessionID]
	if ok {
		m.cleanupSessionDir(cfg)
	}
	delete(m.sessions, sessionID)
}

func (m *SessionSandboxManager) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cfg := range m.sessions {
		m.cleanupSessionDir(cfg)
	}
	m.sessions = make(map[string]*SandboxConfig)
}

func (m *SessionSandboxManager) ListSessions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

func (m *SessionSandboxManager) ApplyToCommand(cmd *exec.Cmd, sessionID string) *exec.Cmd {
	config := m.GetConfig(sessionID)
	return ApplySandbox(cmd, config)
}

func (m *SessionSandboxManager) CleanupSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.sessions[sessionID]
	if ok {
		m.cleanupSessionDir(cfg)
	}
	delete(m.sessions, sessionID)
}

// GetProjectDir returns the project directory this manager was initialized with.
func (m *SessionSandboxManager) GetProjectDir() string {
	return m.projectDir
}

// GetSessionBaseDir returns the session base directory (may be empty).
func (m *SessionSandboxManager) GetSessionBaseDir() string {
	return m.sessionBaseDir
}

// HasSessionIsolation returns true if this manager provides session-level isolation.
func (m *SessionSandboxManager) HasSessionIsolation() bool {
	return m.sessionBaseDir != ""
}

// --- Internal helper methods ---

func (m *SessionSandboxManager) createDefaultSessionConfig(sessionID string) *SandboxConfig {
	if m.sessionBaseDir != "" {
		sessionDir := filepath.Join(m.sessionBaseDir, sessionID)
		tempDir := filepath.Join(sessionDir, "tmp")

		return &SandboxConfig{
			Enabled:      m.defaultConfig.Enabled,
			Profile:      m.defaultConfig.Profile,
			AllowedPaths: []string{m.projectDir, sessionDir},
			AllowNetwork: m.defaultConfig.AllowNetwork,
			CustomPolicy: m.defaultConfig.CustomPolicy,
			TempDir:      tempDir,
			ProjectDir:   m.projectDir,
			SessionDir:   sessionDir,
		}
	}

	// Fallback: no session isolation
	tempDir := filepath.Join(m.defaultConfig.TempDir, sessionID)
	if tempDir == m.defaultConfig.TempDir {
		tempDir = filepath.Join(os.TempDir(), "goreact-sandbox", sessionID)
	}

	return &SandboxConfig{
		Enabled:      m.defaultConfig.Enabled,
		Profile:      m.defaultConfig.Profile,
		AllowedPaths: m.defaultConfig.AllowedPaths,
		AllowNetwork: m.defaultConfig.AllowNetwork,
		CustomPolicy: m.defaultConfig.CustomPolicy,
		TempDir:      tempDir,
		ProjectDir:   m.projectDir,
	}
}

func (m *SessionSandboxManager) createDefaultSessionConfigLocked(sessionID string) *SandboxConfig {
	// Same as createDefaultSessionConfig but assumes lock is held
	if m.sessionBaseDir != "" {
		sessionDir := filepath.Join(m.sessionBaseDir, sessionID)
		tempDir := filepath.Join(sessionDir, "tmp")

		return &SandboxConfig{
			Enabled:      m.defaultConfig.Enabled,
			Profile:      m.defaultConfig.Profile,
			AllowedPaths: []string{m.projectDir, sessionDir},
			AllowNetwork: m.defaultConfig.AllowNetwork,
			CustomPolicy: m.defaultConfig.CustomPolicy,
			TempDir:      tempDir,
			ProjectDir:   m.projectDir,
			SessionDir:   sessionDir,
		}
	}

	tempDir := filepath.Join(m.defaultConfig.TempDir, sessionID)
	if tempDir == m.defaultConfig.TempDir {
		tempDir = filepath.Join(os.TempDir(), "goreact-sandbox", sessionID)
	}

	return &SandboxConfig{
		Enabled:      m.defaultConfig.Enabled,
		Profile:      m.defaultConfig.Profile,
		AllowedPaths: m.defaultConfig.AllowedPaths,
		AllowNetwork: m.defaultConfig.AllowNetwork,
		CustomPolicy: m.defaultConfig.CustomPolicy,
		TempDir:      tempDir,
		ProjectDir:   m.projectDir,
	}
}

func (m *SessionSandboxManager) prepareSessionConfig(sessionID string, input *SandboxConfig) *SandboxConfig {
	if m.sessionBaseDir != "" {
		sessionDir := filepath.Join(m.sessionBaseDir, sessionID)
		tempDir := filepath.Join(sessionDir, "tmp")

		return &SandboxConfig{
			Enabled:      input.Enabled,
			Profile:      input.Profile,
			AllowedPaths: []string{m.projectDir, sessionDir},
			AllowNetwork: input.AllowNetwork,
			CustomPolicy: input.CustomPolicy,
			TempDir:      tempDir,
			ProjectDir:   m.projectDir,
			SessionDir:   sessionDir,
		}
	}

	tempDir := input.TempDir
	if tempDir == "" {
		tempDir = filepath.Join(m.defaultConfig.TempDir, sessionID)
	}

	return &SandboxConfig{
		Enabled:      input.Enabled,
		Profile:      input.Profile,
		AllowedPaths: input.AllowedPaths,
		AllowNetwork: input.AllowNetwork,
		CustomPolicy: input.CustomPolicy,
		TempDir:      tempDir,
		ProjectDir:   m.projectDir,
	}
}

func (m *SessionSandboxManager) copyConfig(src *SandboxConfig) *SandboxConfig {
	return &SandboxConfig{
		Enabled:      src.Enabled,
		Profile:      src.Profile,
		AllowedPaths: src.AllowedPaths,
		AllowNetwork: src.AllowNetwork,
		CustomPolicy: src.CustomPolicy,
		TempDir:      src.TempDir,
		ProjectDir:   src.ProjectDir,
		SessionDir:   src.SessionDir,
	}
}

func (m *SessionSandboxManager) cleanupSessionDir(cfg *SandboxConfig) {
	if cfg != nil && cfg.TempDir != "" {
		os.RemoveAll(cfg.TempDir)
	}
	// Also clean up the entire session directory if it exists
	if cfg != nil && cfg.SessionDir != "" {
		os.RemoveAll(cfg.SessionDir)
	}
}

type SessionContextKey struct{}

func ExtractSessionID(ctx context.Context) string {
	if sessionID, ok := ctx.Value(SessionContextKey{}).(string); ok {
		return sessionID
	}
	return ""
}

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, SessionContextKey{}, sessionID)
}

// GenerateSessionTempDir returns the expected temp directory path for a session.
// This uses SESSION_DIR when available, otherwise falls back to system temp.
func GenerateSessionTempDir(sessionID string, sessionBaseDir string) string {
	if sessionBaseDir != "" {
		sessionDir := filepath.Join(sessionBaseDir, sessionID)
		return filepath.Join(sessionDir, "tmp")
	}
	return filepath.Join(os.TempDir(), "goreact-sandbox", sessionID)
}

// CleanupStaleSessions removes session directories that haven't been modified recently.
// Only applicable when using system temp directory (not SESSION_DIR-based).
func CleanupStaleSessions(baseDir string) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goreact-sandbox")
	}

	if _, err := os.Stat(baseDir); err != nil {
		return
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(baseDir, entry.Name())
			if isStaleSession(fullPath) {
				os.RemoveAll(fullPath)
			}
		}
	}
}

func isStaleSession(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return info.ModTime().Add(24 * 60 * 60 * time.Second).Before(time.Now())
}

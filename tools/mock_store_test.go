package tools

import (
	"context"
	"sync"

	"github.com/DotNetAge/goharness/session"
)

// mockSessionStore 是用于测试的 SessionStore 简单实现
type mockSessionStore struct {
	mu          sync.RWMutex
	messages    map[string][]session.Message
	cursors     map[string]int
	modifyFiles map[string][]string
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		messages:    make(map[string][]session.Message),
		cursors:     make(map[string]int),
		modifyFiles: make(map[string][]string),
	}
}

func (m *mockSessionStore) Append(_ context.Context, sessionID, _, _ string, msg session.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[sessionID] = append(m.messages[sessionID], msg)
	return nil
}

func (m *mockSessionStore) Get(_ context.Context, sessionID string) ([]session.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.messages[sessionID], nil
}

func (m *mockSessionStore) CurrentContext(_ context.Context, _ string, _ int64) ([]session.Message, error) {
	return nil, nil
}

func (m *mockSessionStore) Delete(_ context.Context, _ int64, _ string) error {
	return nil
}

func (m *mockSessionStore) Clear(_ context.Context, _ string) error {
	return nil
}

func (m *mockSessionStore) SetSlideHandler(_ session.SlideHandler) {}

func (m *mockSessionStore) Close() error {
	return nil
}

func (m *mockSessionStore) ListSessions(_ context.Context) ([]session.SessionInfo, error) {
	return nil, nil
}

func (m *mockSessionStore) Create(_ context.Context, _ string, _ ...session.SessionOption) (*session.SessionInfo, error) {
	return &session.SessionInfo{SessionID: "test-session"}, nil
}

func (m *mockSessionStore) GetMeta(_ context.Context, sessionID string) (*session.SessionInfo, error) {
	return &session.SessionInfo{SessionID: sessionID}, nil
}

func (m *mockSessionStore) ResolveSessionDir(_ string) (string, error) {
	return "", nil
}

func (m *mockSessionStore) DeleteSession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.messages, sessionID)
	delete(m.cursors, sessionID)
	delete(m.modifyFiles, sessionID)
	return nil
}

func (m *mockSessionStore) GetCursor(_ context.Context, sessionID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursors[sessionID], nil
}

func (m *mockSessionStore) SetCursor(_ context.Context, sessionID string, cursor int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[sessionID] = cursor
	return nil
}

func (m *mockSessionStore) SaveModifyFiles(sessionID string, files []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modifyFiles[sessionID] = files
	return nil
}

func (m *mockSessionStore) GetModifyFiles(sessionID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.modifyFiles[sessionID], nil
}

func (m *mockSessionStore) UpdateMessages(_ context.Context, sessionID string, cursor int, messages []session.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[sessionID] = messages
	m.cursors[sessionID] = cursor
	return nil
}

func (m *mockSessionStore) Truncate(_ context.Context, _ string, _ int) error {
	return nil
}

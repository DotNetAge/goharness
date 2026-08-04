package loop

import (
	"context"

	"github.com/DotNetAge/goharness/memory"
)

// mockMemory 实现 memory.Memory + LatestRetriever + SessionRetriever 接口用于测试。
type mockMemory struct {
	// LatestResult 控制 RetrieveLatest 返回值
	LatestResult []memory.MemoryChunk
	LatestError  error

	// SessionResult 控制 RetrieveBySession 返回值
	SessionResult []memory.MemoryChunk
	SessionError  error

	// 记录调用参数
	LatestAgent   string
	LatestProject string
	LatestLimit   int

	SessionIDCalled string
	SessionLimit    int
}

func (m *mockMemory) Retrieve(_ context.Context, _ string, _ ...memory.RetrieveOption) ([]memory.MemoryChunk, error) {
	return nil, nil
}

func (m *mockMemory) Store(_ context.Context, _ memory.MemoryChunk) (string, error) {
	return "", nil
}

func (m *mockMemory) Delete(_ context.Context, _ string) error {
	return nil
}

// RetrieveLatest 实现 LatestRetriever 接口
func (m *mockMemory) RetrieveLatest(_ context.Context, agentName, projectDir string, limit int) ([]memory.MemoryChunk, error) {
	m.LatestAgent = agentName
	m.LatestProject = projectDir
	m.LatestLimit = limit
	if m.LatestError != nil {
		return nil, m.LatestError
	}
	return m.LatestResult, nil
}

// RetrieveBySession 实现 SessionRetriever 接口
func (m *mockMemory) RetrieveBySession(_ context.Context, sessionID string, limit int) ([]memory.MemoryChunk, error) {
	m.SessionIDCalled = sessionID
	m.SessionLimit = limit
	if m.SessionError != nil {
		return nil, m.SessionError
	}
	return m.SessionResult, nil
}

// mockMemoryNoLatest 只实现 memory.Memory，不实现 LatestRetriever
type mockMemoryNoLatest struct{}

func (m *mockMemoryNoLatest) Retrieve(_ context.Context, _ string, _ ...memory.RetrieveOption) ([]memory.MemoryChunk, error) {
	return nil, nil
}
func (m *mockMemoryNoLatest) Store(_ context.Context, _ memory.MemoryChunk) (string, error) {
	return "", nil
}
func (m *mockMemoryNoLatest) Delete(_ context.Context, _ string) error { return nil }

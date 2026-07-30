package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
	"github.com/DotNetAge/goharness/tools"
	"gopkg.in/yaml.v3"
)

// fakeSessionStore 是会话存储的内存实现，专用于测试。
type fakeSessionStore struct {
	mu        sync.RWMutex
	messages  map[string][]session.Message
	meta      map[string]*session.SessionInfo
	cursor    map[string]int
	modify    map[string][]string
	appendErr error
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		messages: make(map[string][]session.Message),
		meta:     make(map[string]*session.SessionInfo),
		cursor:   make(map[string]int),
		modify:   make(map[string][]string),
	}
}

func (s *fakeSessionStore) Append(_ context.Context, sessionID, agentName, sponsor string, msg session.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	s.messages[sessionID] = append(s.messages[sessionID], msg)
	return nil
}

func (s *fakeSessionStore) Get(_ context.Context, sessionID string) ([]session.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.messages[sessionID]
	out := make([]session.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

func (s *fakeSessionStore) CurrentContext(_ context.Context, _ string, _ int64) ([]session.Message, error) {
	return nil, nil
}

func (s *fakeSessionStore) Delete(_ context.Context, _ int64, sessionID string) error {
	return nil
}

func (s *fakeSessionStore) Clear(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages, sessionID)
	s.cursor[sessionID] = 0
	return nil
}

func (s *fakeSessionStore) SetSlideHandler(_ session.SlideHandler) {}

func (s *fakeSessionStore) Close() error { return nil }

func (s *fakeSessionStore) ListSessions(_ context.Context) ([]session.SessionInfo, error) { return nil, nil }

func (s *fakeSessionStore) Create(_ context.Context, _ string, _ ...session.SessionOption) (*session.SessionInfo, error) {
	return nil, nil
}

func (s *fakeSessionStore) GetMeta(_ context.Context, sessionID string) (*session.SessionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.meta[sessionID]; ok {
		return m, nil
	}
	return nil, errors.New("未找到会话")
}

func (s *fakeSessionStore) ResolveSessionDir(sessionID string) (string, error) {
	return "/tmp/sessions/" + sessionID, nil
}

func (s *fakeSessionStore) DeleteSession(_ context.Context, _ string) error { return nil }

func (s *fakeSessionStore) GetCursor(_ context.Context, sessionID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cursor[sessionID], nil
}

func (s *fakeSessionStore) SetCursor(_ context.Context, sessionID string, cursor int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor[sessionID] = cursor
	return nil
}

func (s *fakeSessionStore) SaveModifyFiles(sessionID string, files []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modify[sessionID] = files
	return nil
}

func (s *fakeSessionStore) GetModifyFiles(sessionID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modify[sessionID], nil
}

func (s *fakeSessionStore) UpdateMessages(_ context.Context, _ string, _ int, _ []session.Message) error {
	return nil
}

func (s *fakeSessionStore) Truncate(_ context.Context, _ string, _ int) error { return nil }

func (s *fakeSessionStore) ensureMeta(sess *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[sess.ID()] = &session.SessionInfo{
		SessionID:  sess.ID(),
		AgentName:  sess.AgentName(),
		ProjectDir: sess.ProjectDir(),
		SessionDir: sess.SessionDir(),
	}
}

// 编译时断言：fakeKVStore 实现 store.KVStore 接口。
var _ store.KVStore = (*fakeKVStore)(nil)

// fakeKVStore 是键值存储的内存实现，专用于测试。
type fakeKVStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func newFakeKVStore() *fakeKVStore {
	return &fakeKVStore{data: make(map[string]map[string][]byte)}
}

func (k *fakeKVStore) Set(_ context.Context, sessionID, key string, value []byte, _ int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.data[sessionID] == nil {
		k.data[sessionID] = make(map[string][]byte)
	}
	k.data[sessionID][key] = value
	return nil
}

func (k *fakeKVStore) Get(_ context.Context, sessionID, key string) ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.data[sessionID][key], nil
}

func (k *fakeKVStore) Delete(_ context.Context, sessionID, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data[sessionID], key)
	return nil
}

func (k *fakeKVStore) ListKeys(_ context.Context, sessionID string) ([]string, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	var keys []string
	for k := range k.data[sessionID] {
		keys = append(keys, k)
	}
	return keys, nil
}

func (k *fakeKVStore) ClearSession(_ context.Context, sessionID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data, sessionID)
	return nil
}

// fakeTool 是可定制的工具实现，用于测试权限、执行和注册逻辑。
type fakeTool struct {
	info       *tools.ToolInfo
	execute    func(ctx context.Context, params map[string]any) (any, error)
	grant      func(ctx context.Context, params map[string]any) (bool, string)
	isAsync    bool
	invokeCount int
}

func (t *fakeTool) Info() *tools.ToolInfo {
	info := &tools.ToolInfo{}
	if t.info != nil {
		*info = *t.info
	}
	info.Name = t.info.Name
	info.IsAsync = t.isAsync
	return info
}

func (t *fakeTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	t.invokeCount++
	if t.execute != nil {
		return t.execute(ctx, params)
	}
	return fmt.Sprintf("fake result for %s", t.info.Name), nil
}

func (t *fakeTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	if t.grant != nil {
		return t.grant(ctx, params)
	}
	return true, ""
}

func newFakeTool(name string, grantFn func(ctx context.Context, params map[string]any) (bool, string)) *fakeTool {
	return &fakeTool{
		info: &tools.ToolInfo{
			Name:        name,
			Description: "fake " + name,
			Parameters: []tools.Parameter{
				{Name: "value", Type: "string", Description: "value"},
			},
		},
		grant: grantFn,
	}
}

// mockStream 构造一个基于内存事件通道的 gochat Stream。
func mockStream(events []gochatcore.StreamEvent) *gochatcore.Stream {
	ch := make(chan gochatcore.StreamEvent, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return gochatcore.NewStream(ch, nil)
}

// mockLLMClient 是可编程的 LLMClient 实现，用于驱动 ReAct 循环测试。
type mockLLMClient struct {
	responses []*gochatcore.Stream
	calls     int
}

func (m *mockLLMClient) Stream(_ context.Context, _ LLMRequest) (*gochatcore.Stream, error) {
	if m.calls >= len(m.responses) {
		return nil, errors.New("mock LLM 没有更多响应")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func newMockLLMClient(responses ...*gochatcore.Stream) *mockLLMClient {
	return &mockLLMClient{responses: responses}
}

// newTestSession 创建一个绑定到内存存储的测试会话。
func newTestSession(t testingT) *session.Session {
	t.Helper()
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	store.ensureMeta(sess)
	return sess
}

// newTestSessionWithResolver 创建带 modelContextResolver 的测试会话，
// 用于测试依赖 ModelContextLength() 的逻辑（如压缩占位符开关）。
func newTestSessionWithResolver(t testingT, resolver func() int64) *session.Session {
	t.Helper()
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger(),
		session.WithModelContextResolver(resolver),
	)
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	store.ensureMeta(sess)
	return sess
}

// newTestRuntime 创建一个注入了内存依赖的测试 Runtime。
func newTestRuntime(t testingT, opts ...RuntimeConfig) *Runtime {
	t.Helper()
	allOpts := append([]RuntimeConfig{
		WithLogger(logging.NewNopLogger()),
	}, opts...)
	rt := NewRuntime(allOpts...)
	return rt
}

// newTestAgentRegistry 从单个 AgentConfig 创建临时注册表，便于测试注入。
func newTestAgentRegistry(t testingT, cfg config.AgentConfig) *config.AgentRegistry {
	t.Helper()
	dir := t.TempDir()
	var skillsLine string
	if len(cfg.Skills) > 0 {
		data, _ := yaml.Marshal(cfg.Skills)
		skillsLine = "skills:\n" + indent(string(data), 2)
	}
	content := fmt.Sprintf("---\nname: %s\nrole: %s\ndescription: %s\n%s---\n%s",
		cfg.Name, cfg.Role, cfg.Description, skillsLine, cfg.Introduction)
	filePath := filepath.Join(dir, strings.ToLower(cfg.Name)+".md")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试智能体文件失败: %v", err)
	}
	reg, err := config.LoadAgentsFrom(dir, config.WithRegistryLogger(logging.NewNopLogger()))
	if err != nil {
		t.Fatalf("加载测试智能体注册表失败: %v", err)
	}
	return reg
}

// indent 为每一行添加指定数量的空格缩进，最后一行空字符串也处理。
func indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// testingT 抽象 *testing.T 的最小接口，便于在辅助函数中使用。
type testingT interface {
	Helper()
	TempDir() string
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
}

// eventRecorder 记录 AskBuilder 接收到的事件，便于断言事件流。
type eventRecorder struct {
	mu     sync.Mutex
	events []events.ReactEvent
}

func (r *eventRecorder) record(ev events.ReactEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) has(typ events.ReactEventType) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func (r *eventRecorder) count(typ events.ReactEventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

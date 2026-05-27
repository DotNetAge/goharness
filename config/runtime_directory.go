package config

import (
	"sync"
	"time"
)

type RuntimeDirectory struct {
	mu      sync.RWMutex
	agents  map[string]*AgentRuntimeMeta
	maxSize int
}

func NewRuntimeDirectory(maxSize int) *RuntimeDirectory {
	if maxSize <= 0 {
		maxSize = 0
	}
	return &RuntimeDirectory{
		agents:  make(map[string]*AgentRuntimeMeta),
		maxSize: maxSize,
	}
}

func (d *RuntimeDirectory) Register(meta *AgentRuntimeMeta) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.agents[meta.ID()]; exists {
		return ErrRuntimeDirDuplicate
	}
	if d.maxSize > 0 && len(d.agents) >= d.maxSize {
		return ErrRuntimeDirFull
	}
	d.agents[meta.ID()] = meta
	return nil
}

func (d *RuntimeDirectory) Unregister(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.agents, id)
}

func (d *RuntimeDirectory) Get(id string) *AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if meta, ok := d.agents[id]; ok {
		cp := *meta
		return &cp
	}
	return nil
}

func (d *RuntimeDirectory) SetState(id string, state AgentState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.State = state
		meta.LastActive = time.Now()
	}
}

func (d *RuntimeDirectory) SetScore(id string, score float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.Score = score
	}
}

func (d *RuntimeDirectory) ListAll() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*AgentRuntimeMeta, 0, len(d.agents))
	for _, meta := range d.agents {
		cp := *meta
		result = append(result, &cp)
	}
	return result
}

func (d *RuntimeDirectory) ListAvailable() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsAvailable() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

func (d *RuntimeDirectory) ListActive() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

func (d *RuntimeDirectory) FindByDescription(query string) []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() && containsIgnoreCase(meta.Description(), query) {
			cp := *meta
			result = append(result, &cp)
		}
	}
	return result
}

func (d *RuntimeDirectory) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.agents)
}

func (d *RuntimeDirectory) IncrementTaskCount(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.TaskCount++
		meta.LastActive = time.Now()
	}
}

var (
	ErrRuntimeDirDuplicate = newRuntimeErr("agent already registered")
	ErrRuntimeDirFull      = newRuntimeErr("runtime directory full")
	ErrRuntimeDirNotFound  = newRuntimeErr("agent not found")
)

type runtimeErr struct{ msg string }

func newRuntimeErr(msg string) error { return &runtimeErr{msg} }
func (e *runtimeErr) Error() string  { return "runtime directory: " + e.msg }

func sortByScore(agents []*AgentRuntimeMeta) {
	for i := 1; i < len(agents); i++ {
		key := agents[i]
		j := i - 1
		for ; j >= 0 && agents[j].Score < key.Score; j-- {
			agents[j+1] = agents[j]
		}
		agents[j+1] = key
	}
}

func containsIgnoreCase(s, substr string) bool {
	if len(s) == 0 || len(substr) == 0 {
		return false
	}
	sLower := make([]byte, len(s))
	subLower := make([]byte, len(substr))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		sLower[i] = c
	}
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		subLower[i] = c
	}
	for i := 0; i <= len(sLower)-len(subLower); i++ {
		if string(sLower[i:i+len(subLower)]) == string(subLower) {
			return true
		}
	}
	return false
}

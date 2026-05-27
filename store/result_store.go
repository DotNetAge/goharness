package store

import (
	"context"
	"sync"
	"time"
)

const defaultWaitTimeout = 30 * time.Minute

// TaskResult holds the result of an async task execution.
type TaskResult struct {
	TaskID string
	Result string
	Error  string
	Done   bool
}

// ResultStore stores and retrieves async task results.
// collect_results tool blocks on WaitForResult until the task completes.
type ResultStore struct {
	mu      sync.RWMutex
	results map[string]*TaskResult
	waiters map[string][]chan *TaskResult
}

func NewResultStore() *ResultStore {
	return &ResultStore{
		results: make(map[string]*TaskResult),
		waiters: make(map[string][]chan *TaskResult),
	}
}

// Store writes a task result and notifies all waiters.
func (s *ResultStore) Store(taskID string, result *TaskResult) {
	s.mu.Lock()
	s.results[taskID] = result
	waiters := s.waiters[taskID]
	delete(s.waiters, taskID)
	s.mu.Unlock()

	for _, ch := range waiters {
		ch <- result
		close(ch)
	}
}

// WaitForResult blocks until the task completes, then returns the result.
// If the task already completed, returns immediately.
// It respects context cancellation and a default timeout.
func (s *ResultStore) WaitForResult(ctx context.Context, taskID string) *TaskResult {
	s.mu.Lock()
	if r, ok := s.results[taskID]; ok {
		s.mu.Unlock()
		return r
	}

	ch := make(chan *TaskResult, 1)
	s.waiters[taskID] = append(s.waiters[taskID], ch)
	s.mu.Unlock()

	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		return &TaskResult{TaskID: taskID, Error: "context cancelled", Done: false}
	case <-time.After(defaultWaitTimeout):
		return &TaskResult{TaskID: taskID, Error: "timeout waiting for result", Done: false}
	}
}

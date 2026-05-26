package core

import (
	"sync"
	"time"
)

// ContextWindow manages multi-turn conversation context for a session.
// It is safe for concurrent use and supports token-aware pruning.
//
// The Role field binds this window to an agent identity (e.g., "developer",
// "code-reviewer"), so sessions are isolated per-role and never shared
// across different agents.
type ContextWindow struct {
	mu             sync.RWMutex
	SessionID      string    `json:"session_id"`
	Role           string    `json:"role,omitempty"`            // agent identity for session isolation
	Messages       []Message `json:"messages"`
	TokensUsed     int64     `json:"tokens_used"`
	MaxTokens      int64     `json:"max_tokens"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// NewContextWindow creates a new ContextWindow for the given session.
func NewContextWindow(sessionID string, maxTokens int64) *ContextWindow {
	return NewContextWindowWithRole(sessionID, "", maxTokens)
}

// NewContextWindowWithRole creates a new ContextWindow bound to a specific agent role.
// The role is used to isolate sessions — each role has its own independent session
// history so that switching agents does not leak context between roles.
func NewContextWindowWithRole(sessionID string, role string, maxTokens int64) *ContextWindow {
	now := time.Now()
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return &ContextWindow{
		SessionID:      sessionID,
		Role:           role,
		Messages:       make([]Message, 0),
		MaxTokens:      maxTokens,
		CreatedAt:      now,
		LastActivityAt: now,
	}
}

// AddMessage appends a message to the context window.
func (w *ContextWindow) AddMessage(role, content string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Messages = append(w.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().Unix(),
	})
	w.LastActivityAt = time.Now()
}

// AddMessageWithTimestamp appends a message with a specific timestamp.
func (w *ContextWindow) AddMessageWithTimestamp(role, content string, ts int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Messages = append(w.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: ts,
	})
	w.LastActivityAt = time.Now()
}

// Messages returns a copy of all messages in the window.
func (w *ContextWindow) GetMessages() []Message {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Message, len(w.Messages))
	copy(out, w.Messages)
	return out
}

// RecentMessages returns the last N messages for prompt injection.
// If n <= 0, returns all messages.
func (w *ContextWindow) RecentMessages(n int) []Message {
	w.mu.RLock()
	defer w.mu.RUnlock()
	msgs := w.Messages
	if n > 0 && len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out
}

// AddTokens accumulates token usage for tracking context window budget.
func (w *ContextWindow) AddTokens(n int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.TokensUsed += n
}

// TokensRemaining returns estimated remaining tokens.
func (w *ContextWindow) TokensRemaining() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	remaining := w.MaxTokens - w.TokensUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Reset clears all messages and token counts, starting fresh.
func (w *ContextWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Messages = make([]Message, 0)
	w.TokensUsed = 0
	w.LastActivityAt = time.Now()
}

// MessageCount returns the number of messages in the window.
func (w *ContextWindow) MessageCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.Messages)
}

// ---------------------------------------------------------------------------
// Sliding Window Support
// ---------------------------------------------------------------------------

// SlideConfig controls when and how the context window slides out old messages.
type SlideConfig struct {
	SlideTriggerRatio   float64
	TargetRatio         float64
	MinPreserveMessages int
	MaxSlideBatch       int
}

// DefaultSlideConfig returns sensible defaults for Think-Act agent workloads.
// Trigger at 65% usage (before "Lost in the Middle" ~70%), target 45% after slide,
// preserve minimum 4 messages (2 full turns).
var DefaultSlideConfig = SlideConfig{
	SlideTriggerRatio:   0.65,
	TargetRatio:         0.45,
	MinPreserveMessages: 4,
	MaxSlideBatch:       0,
}

// SlidedMessages records the result of a Slide operation.
type SlidedMessages struct {
	Messages   []Message
	TokenCount int64
}

// UsageRatio returns current token usage as a ratio of MaxTokens (0.0–1.0+).
func (w *ContextWindow) UsageRatio() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.MaxTokens <= 0 {
		return 0
	}
	return float64(w.TokensUsed) / float64(w.MaxTokens)
}

// SlideTriggered returns true if token usage meets or exceeds the trigger ratio.
func (w *ContextWindow) SlideTriggered(config SlideConfig) bool {
	return w.UsageRatio() >= config.SlideTriggerRatio
}

// calculateTotalTokens estimates total tokens across all messages using the given function.
func (w *ContextWindow) calculateTotalTokens(estimateFn func(string) int) int64 {
	var total int64
	for _, m := range w.Messages {
		total += int64(estimateFn(m.Content))
	}
	return total
}

// Slide batch-removes the oldest non-system messages until token usage drops to
// TargetRatio. It always preserves at least MinPreserveMessages non-system messages.
// System role messages are NEVER evicted — they are preserved in place and skipped
// during sliding. Returns the evicted messages so callers can forward them to
// RAG/Memory.
//
// Performance: O(n*m) where n is evicted count and m is scan depth for the next
// non-system message; typically small since system messages are at the front.
func (w *ContextWindow) Slide(config SlideConfig, estimateFn func(string) int) SlidedMessages {
	w.mu.Lock()
	defer w.mu.Unlock()

	estimate := estimateFn
	if estimate == nil {
		estimate = func(s string) int { return len(s)/4 + 1 }
	}

	targetTokens := int64(float64(w.MaxTokens) * config.TargetRatio)
	var slided []Message
	var slidedTokens int64

	totalTokens := w.calculateTotalTokens(estimate)

	// Count non-system messages for the preserve check
	nonSystemCount := 0
	for _, m := range w.Messages {
		if m.Role != "system" {
			nonSystemCount++
		}
	}

	for nonSystemCount > config.MinPreserveMessages && len(w.Messages) > 0 {
		if totalTokens <= targetTokens {
			break
		}

		// Find the first non-system message to evict.
		// System messages are always preserved in place during sliding.
		evictIdx := -1
		for i, m := range w.Messages {
			if m.Role != "system" {
				evictIdx = i
				break
			}
		}
		if evictIdx < 0 {
			break
		}

		removed := w.Messages[evictIdx]
		removedTokens := int64(estimate(removed.Content))

		w.Messages = append(w.Messages[:evictIdx], w.Messages[evictIdx+1:]...)
		slided = append(slided, removed)
		slidedTokens += removedTokens

		totalTokens -= removedTokens
		nonSystemCount--

		if config.MaxSlideBatch > 0 && len(slided) >= config.MaxSlideBatch {
			break
		}
	}

	w.TokensUsed = totalTokens
	w.LastActivityAt = time.Now()

	return SlidedMessages{Messages: slided, TokenCount: slidedTokens}
}

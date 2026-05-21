package core

import "sync"

// ContentReplacementState tracks which tool results have been processed
// and which have been replaced with previews or suppressed.
//
// Once a tool_use_id is "seen", the decision is frozen permanently.
// This ensures prompt cache stability across T-A-O cycles.
type ContentReplacementState struct {
	mu           sync.Mutex
	seenIDs      map[string]bool    // toolUseID → seen (executed or replaced)
	replacements map[string]string  // toolUseID → replacement string (empty if only executed)
}

func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{
		seenIDs:      make(map[string]bool),
		replacements: make(map[string]string),
	}
}

// IsFresh returns true if the toolUseID has not been seen before.
func (s *ContentReplacementState) IsFresh(toolUseID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.seenIDs[toolUseID]
}

// MarkExecuted records that a tool result was processed and kept as-is.
func (s *ContentReplacementState) MarkExecuted(toolUseID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenIDs[toolUseID] = true
}

// MarkReplaced records that a tool result was replaced with a preview or suppression marker.
func (s *ContentReplacementState) MarkReplaced(toolUseID, replacement string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenIDs[toolUseID] = true
	s.replacements[toolUseID] = replacement
}

// GetReplacement returns the cached replacement string for a toolUseID.
func (s *ContentReplacementState) GetReplacement(toolUseID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.replacements[toolUseID]
	return r, ok
}

// IsReplaced returns true if the toolUseID has been replaced.
func (s *ContentReplacementState) IsReplaced(toolUseID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.replacements[toolUseID]
	return ok
}

// Clone creates an independent copy of the state (used for child agents in CloneReactor).
func (s *ContentReplacementState) Clone() *ContentReplacementState {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := &ContentReplacementState{
		seenIDs:      make(map[string]bool, len(s.seenIDs)),
		replacements: make(map[string]string, len(s.replacements)),
	}
	for k, v := range s.seenIDs {
		clone.seenIDs[k] = v
	}
	for k, v := range s.replacements {
		clone.replacements[k] = v
	}
	return clone
}

// ReconstructFromHistory rebuilds the state from stored conversation history.
// Used when reloading a session.
func (s *ContentReplacementState) ReconstructFromHistory(history []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range history {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			s.seenIDs[msg.ToolCallID] = true
		}
	}
}

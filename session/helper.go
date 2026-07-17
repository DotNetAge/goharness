package session

import "context"

// ── Store-level operations ──────────────────────────────────────────────
// These functions wrap SessionStore methods so that external code never
// calls SessionStore methods directly. All session operations go through
// the session package.

// ListSessions returns all sessions from the store.
func ListSessions(ctx context.Context, store SessionStore) ([]SessionInfo, error) {
	return store.ListSessions(ctx)
}

// CreateSession creates a new session in the store with the given agent name and options.
func CreateSession(ctx context.Context, store SessionStore, agentName string, opts ...SessionOption) (*SessionInfo, error) {
	return store.Create(ctx, agentName, opts...)
}

// DeleteSession removes a session from the store.
func DeleteSession(ctx context.Context, store SessionStore, sessionID string) error {
	return store.DeleteSession(ctx, sessionID)
}

// GetSessionMeta returns session metadata from the store.
func GetSessionMeta(ctx context.Context, store SessionStore, sessionID string) (*SessionInfo, error) {
	return store.GetMeta(ctx, sessionID)
}

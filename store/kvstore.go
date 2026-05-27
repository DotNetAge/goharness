package store

import "context"

// KVStore defines the interface for session-scoped key-value storage.
// Implementations provide isolated KV storage per session with support for
// optional TTL (time-to-live) on stored values.
type KVStore interface {
	// Set stores a key-value pair in the given session.
	// ttlSeconds controls expiration: 0 = no expiry, >0 = expire after N seconds, <0 = immediately expire.
	Set(ctx context.Context, sessionID, key string, value []byte, ttlSeconds int) error
	// Get retrieves the value for a key from the given session.
	// Returns nil if the key doesn't exist or has expired.
	Get(ctx context.Context, sessionID, key string) ([]byte, error)
	// Delete removes a key from the given session.
	Delete(ctx context.Context, sessionID, key string) error
	// ListKeys returns all keys in the given session.
	ListKeys(ctx context.Context, sessionID string) ([]string, error)
	// ClearSession removes all keys associated with a session.
	ClearSession(ctx context.Context, sessionID string) error
}

package cache

import (
	"context"
	"time"
)

// Store holds values under keys until they expire. Implementations must be safe for concurrent
// use. A missing or expired key is not an error: Get returns found false.
type Store interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	// Set stores a value for ttl; a ttl of zero or less stores it until the store drops it.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete removes keys; keys that do not exist are ignored.
	Delete(ctx context.Context, keys ...string) error
}

// PrefixDeleter is implemented by stores that can remove every key that starts with a prefix,
// which DeletePrefix and Clear use.
type PrefixDeleter interface {
	DeletePrefix(ctx context.Context, prefix string) error
}

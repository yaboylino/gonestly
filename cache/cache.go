package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

// Options configures a Cache.
type Options struct {
	// Store holds the values; a memory store of 64 MiB when nil.
	Store Store
	// Prefix is put before every key, so applications can share a store such as Redis without
	// meeting each other's keys.
	Prefix string
	// DefaultTTL is how long a value lives when the call does not say; 5 minutes when zero.
	DefaultTTL time.Duration
	// Logger receives the problems of the store; slog.Default() when nil.
	Logger *slog.Logger
}

// Cache stores typed values in a Store, loads them once when many ask at the same moment, and
// treats a failing store as an empty one: the cache is an optimization, so a request does not
// fail because it is down. It is safe for concurrent use.
type Cache struct {
	store  Store
	owned  bool
	prefix string
	ttl    time.Duration
	logger *slog.Logger
	flight flight

	hits, misses, loads, errs atomic.Int64
}

// New creates a Cache.
func New(opts Options) (*Cache, error) {
	if opts.DefaultTTL < 0 {
		return nil, errors.New("cache: DefaultTTL cannot be negative")
	}
	c := &Cache{store: opts.Store, prefix: opts.Prefix, ttl: opts.DefaultTTL, logger: opts.Logger}
	if c.store == nil {
		memory, err := NewMemory(MemoryOptions{})
		if err != nil {
			return nil, err
		}
		c.store, c.owned = memory, true
	}
	if c.ttl == 0 {
		c.ttl = 5 * time.Minute
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c, nil
}

// Stats counts what the cache did since it was created.
type Stats struct {
	// Hits and Misses count lookups that found a value and that did not.
	Hits, Misses int64
	// Loads counts how often a loader ran, which is less than Misses when callers shared one.
	Loads int64
	// Errors counts failures of the store, which were logged and otherwise ignored.
	Errors int64
}

// Stats returns the counters.
func (c *Cache) Stats() Stats {
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Loads: c.loads.Load(), Errors: c.errs.Load()}
}

// Store returns the store, for code that needs more than the typed calls.
func (c *Cache) Store() Store { return c.store }

func (c *Cache) key(key string) string { return c.prefix + key }

// OnStop closes the store when the cache created it, or when it can be closed.
func (c *Cache) OnStop(ctx context.Context) error {
	switch s := c.store.(type) {
	case interface{ OnStop(context.Context) error }:
		return s.OnStop(ctx)
	case io.Closer:
		if c.owned {
			return s.Close()
		}
	}
	return nil
}

// Get returns the value under key. found is false for a missing value, and also when the store
// failed, which is logged. A stored value that does not fit T is removed and counts as missing,
// so a change to a type does not break a deploy.
func Get[T any](ctx context.Context, c *Cache, key string) (value T, found bool) {
	data, ok, err := c.store.Get(ctx, c.key(key))
	if err != nil {
		c.fail(ctx, "reading", key, err)
		c.misses.Add(1)
		return value, false
	}
	if !ok {
		c.misses.Add(1)
		return value, false
	}
	if err := json.Unmarshal(data, &value); err != nil {
		var zero T
		_ = c.store.Delete(ctx, c.key(key))
		c.misses.Add(1)
		return zero, false
	}
	c.hits.Add(1)
	return value, true
}

// Set stores a value for ttl, or for DefaultTTL when ttl is zero. A store that fails is logged;
// an error is returned only when the value cannot be encoded.
func Set[T any](ctx context.Context, c *Cache, key string, value T, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: encoding the value of %q: %w", key, err)
	}
	if ttl == 0 {
		ttl = c.ttl
	}
	if err := c.store.Set(ctx, c.key(key), data, ttl); err != nil {
		c.fail(ctx, "writing", key, err)
	}
	return nil
}

// Remember returns the value under key, or calls load, stores what it returned for ttl and
// returns that. When many callers ask for the same missing key at the same moment, load runs once
// and they share the result, which keeps a popular value from being computed by every request
// while it is gone. An error from load is returned and not stored.
//
// load gets a context that has the values of ctx but does not end with it, so one caller that
// gives up does not fail the others; a caller that gives up returns at once with its own error.
func Remember[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, load func(ctx context.Context) (T, error)) (T, error) {
	if value, found := Get[T](ctx, c, key); found {
		return value, nil
	}
	shared, err := c.flight.do(ctx, c.key(key), func() (any, error) {
		loadCtx := context.WithoutCancel(ctx)
		// Another caller may have stored it while this one was starting.
		if value, found := Get[T](loadCtx, c, key); found {
			return value, nil
		}
		c.loads.Add(1)
		value, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		if err := Set(loadCtx, c, key, value, ttl); err != nil {
			return nil, err
		}
		return value, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	value, ok := shared.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("cache: the key %q is loaded as two different types", key)
	}
	return value, nil
}

// Delete removes keys, for example after the data behind them changed.
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	prefixed := make([]string, len(keys))
	for i, key := range keys {
		prefixed[i] = c.key(key)
	}
	return c.store.Delete(ctx, prefixed...)
}

// DeletePrefix removes every key that starts with prefix, such as "user:" after any user
// changed. It returns an error when the store cannot do that.
func (c *Cache) DeletePrefix(ctx context.Context, prefix string) error {
	deleter, ok := c.store.(PrefixDeleter)
	if !ok {
		return fmt.Errorf("cache: %T cannot delete by prefix", c.store)
	}
	return deleter.DeletePrefix(ctx, c.key(prefix))
}

// Clear removes every key of this cache, that is every key with its Prefix.
func (c *Cache) Clear(ctx context.Context) error { return c.DeletePrefix(ctx, "") }

func (c *Cache) fail(ctx context.Context, doing, key string, err error) {
	c.errs.Add(1)
	c.logger.WarnContext(ctx, "cache store failed, continuing without it", slog.String("while", doing), slog.String("key", key), slog.Any("error", err))
}

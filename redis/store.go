package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Store keeps cache entries in Redis. It implements cache.Store, cache.PrefixDeleter and
// gonestly.HealthChecker.
type Store struct {
	client goredis.UniversalClient
	owned  bool
	once   sync.Once
}

// NewStore uses a client that you manage: the store does not close it.
func NewStore(client goredis.UniversalClient) *Store {
	return &Store{client: client}
}

// Open connects to Redis with a URL such as redis://:password@localhost:6379/0, or rediss:// for
// TLS, and checks the connection. The store owns the connection and closes it when the
// application stops.
func Open(ctx context.Context, url string) (*Store, error) {
	options, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redis: reading the URL: %w", err)
	}
	client := goredis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: connecting to %s: %w", options.Addr, err)
	}
	return &Store{client: client, owned: true}, nil
}

// Get returns the value of a key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := s.client.Get(ctx, key).Bytes()
	switch {
	case errors.Is(err, goredis.Nil):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return value, true, nil
}

// Set stores a value for ttl; without a ttl it stays until Redis evicts it.
func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < 0 {
		ttl = 0
	}
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Delete removes keys.
func (s *Store) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.client.Unlink(ctx, keys...).Err()
}

// DeletePrefix removes every key that starts with prefix. It scans the keyspace in batches, so
// it does not block Redis, but it is meant for invalidation now and then, not for every request.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	pattern := escapePattern(prefix) + "*"
	scan := func(ctx context.Context, client *goredis.Client) error {
		var batch []string
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			err := client.Unlink(ctx, batch...).Err()
			batch = batch[:0]
			return err
		}
		iter := client.Scan(ctx, 0, pattern, 500).Iterator()
		for key := range iter.Vals(ctx) {
			batch = append(batch, key)
			if len(batch) >= 500 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := iter.Err(); err != nil {
			return err
		}
		return flush()
	}
	if cluster, ok := s.client.(*goredis.ClusterClient); ok {
		return cluster.ForEachMaster(ctx, scan)
	}
	if client, ok := s.client.(*goredis.Client); ok {
		return scan(ctx, client)
	}
	return errors.New("redis: deleting by prefix needs a *redis.Client or a *redis.ClusterClient")
}

// escapePattern makes characters that mean something in a MATCH pattern literal.
func escapePattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(s)
}

// CheckHealth pings Redis, so that the application reports it unavailable when Redis is.
func (s *Store) CheckHealth(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// OnStop closes the connection when Open made it.
func (s *Store) OnStop(context.Context) error {
	var err error
	if s.owned {
		s.once.Do(func() { err = s.client.Close() })
	}
	return err
}

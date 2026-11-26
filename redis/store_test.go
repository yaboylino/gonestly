package redis_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/yaboylino/gonestly/cache"
	"github.com/yaboylino/gonestly/cache/cachetest"
	"github.com/yaboylino/gonestly/redis"
)

const redisEnv = "GONESTLY_TEST_REDIS"

func redisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv(redisEnv)
	if url == "" {
		t.Skipf("set %s to a Redis URL to run the Redis tests", redisEnv)
	}
	return url
}

// prefixed gives each test its own corner of a shared Redis.
type prefixed struct {
	*redis.Store
	prefix string
}

func (p prefixed) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return p.Store.Get(ctx, p.prefix+key)
}

func (p prefixed) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return p.Store.Set(ctx, p.prefix+key, value, ttl)
}

func (p prefixed) Delete(ctx context.Context, keys ...string) error {
	full := make([]string, len(keys))
	for i, key := range keys {
		full[i] = p.prefix + key
	}
	return p.Store.Delete(ctx, full...)
}

func (p prefixed) DeletePrefix(ctx context.Context, prefix string) error {
	return p.Store.DeletePrefix(ctx, p.prefix+prefix)
}

func open(t *testing.T) *redis.Store {
	t.Helper()
	store, err := redis.Open(t.Context(), redisURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.OnStop(context.Background()) })
	return store
}

func TestRedisMeetsTheStoreContract(t *testing.T) {
	redisURL(t)
	cachetest.Run(t, func(t *testing.T) cache.Store {
		return prefixed{Store: open(t), prefix: "test:" + strings.ToLower(rand.Text()) + ":"}
	})
}

func TestInstancesShareTheCache(t *testing.T) {
	prefix := "shared:" + strings.ToLower(rand.Text()) + ":"
	one, err := cache.New(cache.Options{Store: open(t), Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	two, err := cache.New(cache.Options{Store: open(t), Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	load := func(context.Context) (string, error) { loads++; return "value", nil }
	if v, err := cache.Remember(t.Context(), one, "k", time.Minute, load); err != nil || v != "value" {
		t.Fatalf("first instance: %q, %v", v, err)
	}
	if v, err := cache.Remember(t.Context(), two, "k", time.Minute, load); err != nil || v != "value" || loads != 1 {
		t.Errorf("second instance: %q, %v, %d loads", v, err, loads)
	}
	if err := one.DeletePrefix(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, found := cache.Get[string](t.Context(), two, "k"); found {
		t.Error("the second instance still sees a deleted value")
	}
}

func TestHealthAndStop(t *testing.T) {
	store := open(t)
	if err := store.CheckHealth(t.Context()); err != nil {
		t.Errorf("CheckHealth: %v", err)
	}
	if err := store.OnStop(t.Context()); err != nil {
		t.Errorf("OnStop: %v", err)
	}
	if err := store.OnStop(t.Context()); err != nil {
		t.Errorf("a second OnStop: %v", err)
	}
	if err := store.CheckHealth(t.Context()); err == nil {
		t.Error("an owned store that was stopped still answers")
	}
}

func TestANewStoreDoesNotCloseTheClient(t *testing.T) {
	options, err := goredis.ParseURL(redisURL(t))
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(options)
	defer client.Close()
	store := redis.NewStore(client)
	if err := store.OnStop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Errorf("the client of the caller was closed: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := redis.Open(t.Context(), "http://localhost"); err == nil || !strings.Contains(err.Error(), "reading the URL") {
		t.Errorf("a bad URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := redis.Open(ctx, "redis://127.0.0.1:1/0"); err == nil || !strings.Contains(err.Error(), "connecting to 127.0.0.1:1") {
		t.Errorf("an unreachable server: %v", err)
	}
}

func TestACacheOnAStoppedRedisKeepsWorking(t *testing.T) {
	store := open(t)
	c, err := cache.New(cache.Options{Store: store, Prefix: "down:"})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.OnStop(t.Context())
	v, err := cache.Remember(t.Context(), c, "k", time.Minute, func(context.Context) (int, error) { return 5, nil })
	if err != nil || v != 5 {
		t.Errorf("Remember with Redis down: %d, %v", v, err)
	}
	if c.Stats().Errors == 0 {
		t.Error("the failures of the store were not counted")
	}
}

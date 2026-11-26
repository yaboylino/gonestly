package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type profile struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func newCache(t *testing.T, opts Options) *Cache {
	t.Helper()
	opts.Logger = slog.New(slog.DiscardHandler)
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.OnStop(context.Background()) })
	return c
}

func TestGetAndSet(t *testing.T) {
	c := newCache(t, Options{})
	ctx := t.Context()
	if _, found := Get[profile](ctx, c, "p"); found {
		t.Fatal("a missing key was found")
	}
	if err := Set(ctx, c, "p", profile{Name: "Ada", Age: 36}, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, found := Get[profile](ctx, c, "p")
	if !found || got != (profile{Name: "Ada", Age: 36}) {
		t.Errorf("Get = %+v, %v", got, found)
	}
	if s := c.Stats(); s.Hits != 1 || s.Misses != 1 {
		t.Errorf("Stats = %+v", s)
	}
	if err := Set(ctx, c, "bad", make(chan int), 0); err == nil {
		t.Error("a value that cannot be encoded was accepted")
	}
	if err := c.Delete(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if _, found := Get[profile](ctx, c, "p"); found {
		t.Error("a deleted key was found")
	}
}

func TestAValueOfAnotherTypeIsDroppedNotAnError(t *testing.T) {
	c := newCache(t, Options{})
	_ = Set(t.Context(), c, "k", "a string", time.Minute)
	if got, found := Get[profile](t.Context(), c, "k"); found {
		t.Errorf("found %+v", got)
	}
	if _, found := Get[string](t.Context(), c, "k"); found {
		t.Error("the value that did not fit was kept")
	}
}

func TestPrefixAndClear(t *testing.T) {
	store, _ := NewMemory(MemoryOptions{})
	defer store.Close()
	shop := newCache(t, Options{Store: store, Prefix: "shop:"})
	blog := newCache(t, Options{Store: store, Prefix: "blog:"})
	ctx := t.Context()
	_ = Set(ctx, shop, "user:1", 1, 0)
	_ = Set(ctx, shop, "user:2", 2, 0)
	_ = Set(ctx, shop, "item:1", 3, 0)
	_ = Set(ctx, blog, "user:1", 4, 0)
	if got, _ := Get[int](ctx, shop, "user:1"); got != 1 {
		t.Errorf("shop user:1 = %d", got)
	}
	if got, _ := Get[int](ctx, blog, "user:1"); got != 4 {
		t.Errorf("the prefixes did not separate the caches: %d", got)
	}
	if err := shop.DeletePrefix(ctx, "user:"); err != nil {
		t.Fatal(err)
	}
	if _, found := Get[int](ctx, shop, "user:2"); found {
		t.Error("user:2 survived DeletePrefix")
	}
	if _, found := Get[int](ctx, shop, "item:1"); !found {
		t.Error("item:1 was deleted by another prefix")
	}
	if err := shop.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found := Get[int](ctx, shop, "item:1"); found {
		t.Error("Clear left a key")
	}
	if got, _ := Get[int](ctx, blog, "user:1"); got != 4 {
		t.Error("Clear removed the keys of another cache")
	}
}

type plainStore struct{ Store }

func TestDeletePrefixNeedsAStoreThatCan(t *testing.T) {
	memory, _ := NewMemory(MemoryOptions{})
	defer memory.Close()
	c := newCache(t, Options{Store: plainStore{memory}})
	if err := c.DeletePrefix(t.Context(), "x"); err == nil || !strings.Contains(err.Error(), "cannot delete by prefix") {
		t.Errorf("err = %v", err)
	}
}

func TestRememberLoadsOnce(t *testing.T) {
	c := newCache(t, Options{})
	var loads atomic.Int32
	load := func(context.Context) (profile, error) {
		if loads.Add(1) > 100 {
			return profile{}, errors.New("far too many loads")
		}
		time.Sleep(50 * time.Millisecond)
		return profile{Name: "Ada"}, nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := Remember(t.Context(), c, "p", time.Minute, load)
			if err != nil || got.Name != "Ada" {
				t.Errorf("Remember = %+v, %v", got, err)
			}
		}()
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Errorf("the loader ran %d times for 50 callers", loads.Load())
	}
	// And once it is stored, the loader is not called at all.
	if _, err := Remember(t.Context(), c, "p", time.Minute, load); err != nil || loads.Load() != 1 {
		t.Errorf("a cached value was loaded again: %d, %v", loads.Load(), err)
	}
	if s := c.Stats(); s.Loads != 1 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestRememberDoesNotStoreErrors(t *testing.T) {
	c := newCache(t, Options{})
	boom := errors.New("the database is gone")
	calls := 0
	for range 2 {
		_, err := Remember(t.Context(), c, "p", time.Minute, func(context.Context) (int, error) { calls++; return 0, boom })
		if !errors.Is(err, boom) {
			t.Errorf("err = %v", err)
		}
	}
	if calls != 2 {
		t.Errorf("the error was cached: %d calls", calls)
	}
	if v, err := Remember(t.Context(), c, "p", time.Minute, func(context.Context) (int, error) { return 7, nil }); err != nil || v != 7 {
		t.Errorf("after the failure: %d, %v", v, err)
	}
}

func TestRememberExpires(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store, _ := NewMemory(MemoryOptions{Now: clock.Now, SweepInterval: time.Hour})
	defer store.Close()
	c := newCache(t, Options{Store: store, DefaultTTL: time.Minute})
	n := 0
	load := func(context.Context) (int, error) { n++; return n, nil }
	first, _ := Remember(t.Context(), c, "k", 0, load)
	again, _ := Remember(t.Context(), c, "k", 0, load)
	clock.Advance(2 * time.Minute)
	later, _ := Remember(t.Context(), c, "k", 0, load)
	if first != 1 || again != 1 || later != 2 {
		t.Errorf("values %d %d %d, want 1 1 2", first, again, later)
	}
}

func TestACallerThatGivesUpDoesNotFailTheOthers(t *testing.T) {
	c := newCache(t, Options{})
	release := make(chan struct{})
	started := make(chan struct{})
	load := func(ctx context.Context) (string, error) {
		close(started)
		<-release
		return "done", ctx.Err()
	}
	quitting, cancel := context.WithCancel(t.Context())
	quit := make(chan error, 1)
	go func() {
		_, err := Remember(quitting, c, "k", time.Minute, load)
		quit <- err
	}()
	<-started
	other := make(chan string, 1)
	go func() {
		v, _ := Remember(t.Context(), c, "k", time.Minute, func(context.Context) (string, error) { return "second loader", nil })
		other <- v
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-quit; !errors.Is(err, context.Canceled) {
		t.Errorf("the caller that gave up got %v", err)
	}
	close(release)
	select {
	case v := <-other:
		if v != "done" {
			t.Errorf("the other caller got %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the other caller never got a value")
	}
}

func TestALoaderThatPanicsIsAnError(t *testing.T) {
	c := newCache(t, Options{})
	_, err := Remember(t.Context(), c, "k", time.Minute, func(context.Context) (int, error) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if v, err := Remember(t.Context(), c, "k", time.Minute, func(context.Context) (int, error) { return 3, nil }); err != nil || v != 3 {
		t.Errorf("after the panic: %d, %v", v, err)
	}
}

type brokenStore struct{ calls atomic.Int32 }

func (s *brokenStore) Get(context.Context, string) ([]byte, bool, error) {
	s.calls.Add(1)
	return nil, false, errors.New("connection refused")
}

func (s *brokenStore) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("connection refused")
}

func (s *brokenStore) Delete(context.Context, ...string) error {
	return errors.New("connection refused")
}

func TestAFailingStoreIsAnEmptyStore(t *testing.T) {
	c := newCache(t, Options{Store: &brokenStore{}})
	got, err := Remember(t.Context(), c, "k", time.Minute, func(context.Context) (string, error) { return "loaded", nil })
	if err != nil || got != "loaded" {
		t.Errorf("Remember = %q, %v", got, err)
	}
	if _, found := Get[string](t.Context(), c, "k"); found {
		t.Error("found a value in a broken store")
	}
	if err := Set(t.Context(), c, "k", "v", 0); err != nil {
		t.Errorf("Set returned the failure of the store: %v", err)
	}
	if s := c.Stats(); s.Errors < 2 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestOptionsAreChecked(t *testing.T) {
	if _, err := New(Options{DefaultTTL: -1}); err == nil {
		t.Error("a negative ttl")
	}
	c, err := New(Options{})
	if err != nil || c.ttl != 5*time.Minute || c.Store() == nil {
		t.Errorf("defaults: %+v, %v", c, err)
	}
	_ = c.OnStop(t.Context())
}

type closingStore struct {
	Store
	closed atomic.Bool
}

func (s *closingStore) Close() error { s.closed.Store(true); return nil }

func TestOnStopClosesOnlyAStoreItOwns(t *testing.T) {
	memory, _ := NewMemory(MemoryOptions{})
	given := &closingStore{Store: memory}
	c, _ := New(Options{Store: given})
	_ = c.OnStop(t.Context())
	if given.closed.Load() {
		t.Error("a store that was given was closed")
	}
	_ = memory.Close()

	owned, _ := New(Options{})
	if err := owned.OnStop(t.Context()); err != nil {
		t.Error(err)
	}
	if err := owned.OnStop(t.Context()); err != nil {
		t.Errorf("a second OnStop: %v", err)
	}
}

func TestKeysWithManyTypes(t *testing.T) {
	c := newCache(t, Options{})
	_ = Set(t.Context(), c, "list", []int{1, 2, 3}, 0)
	_ = Set(t.Context(), c, "map", map[string]int{"a": 1}, 0)
	_ = Set(t.Context(), c, "nothing", (*profile)(nil), 0)
	if got, _ := Get[[]int](t.Context(), c, "list"); fmt.Sprint(got) != "[1 2 3]" {
		t.Errorf("list = %v", got)
	}
	if got, _ := Get[map[string]int](t.Context(), c, "map"); got["a"] != 1 {
		t.Errorf("map = %v", got)
	}
	if got, found := Get[*profile](t.Context(), c, "nothing"); !found || got != nil {
		t.Errorf("a cached nil = %v, %v", got, found)
	}
}

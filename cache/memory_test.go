package cache

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newMemory(t *testing.T, opts MemoryOptions) (*Memory, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	opts.Now = clock.Now
	if opts.SweepInterval == 0 {
		opts.SweepInterval = time.Hour
	}
	m, err := NewMemory(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, clock
}

func TestMemoryGetSetDelete(t *testing.T) {
	m, _ := newMemory(t, MemoryOptions{})
	ctx := t.Context()
	if _, found, err := m.Get(ctx, "a"); found || err != nil {
		t.Fatalf("a missing key: %v %v", found, err)
	}
	_ = m.Set(ctx, "a", []byte("1"), time.Minute)
	if v, found, _ := m.Get(ctx, "a"); !found || string(v) != "1" {
		t.Errorf("Get = %q, %v", v, found)
	}
	_ = m.Set(ctx, "a", []byte("2"), time.Minute)
	if v, _, _ := m.Get(ctx, "a"); string(v) != "2" || m.Len() != 1 {
		t.Errorf("overwrite: %q, %d entries", v, m.Len())
	}
	_ = m.Delete(ctx, "a", "never there")
	if _, found, _ := m.Get(ctx, "a"); found {
		t.Error("a deleted key is still there")
	}
}

func TestMemoryCopiesValues(t *testing.T) {
	m, _ := newMemory(t, MemoryOptions{})
	value := []byte("abc")
	_ = m.Set(t.Context(), "k", value, 0)
	value[0] = 'X'
	got, _, _ := m.Get(t.Context(), "k")
	got[1] = 'Y'
	if again, _, _ := m.Get(t.Context(), "k"); string(again) != "abc" {
		t.Errorf("the stored value changed: %q", again)
	}
}

func TestMemoryExpiry(t *testing.T) {
	m, clock := newMemory(t, MemoryOptions{})
	ctx := t.Context()
	_ = m.Set(ctx, "short", []byte("1"), time.Minute)
	_ = m.Set(ctx, "forever", []byte("2"), 0)
	clock.Advance(59 * time.Second)
	if _, found, _ := m.Get(ctx, "short"); !found {
		t.Error("expired too early")
	}
	clock.Advance(2 * time.Second)
	if _, found, _ := m.Get(ctx, "short"); found {
		t.Error("an expired key was returned")
	}
	if _, found, _ := m.Get(ctx, "forever"); !found {
		t.Error("a key without ttl expired")
	}
	if m.Len() != 1 {
		t.Errorf("%d entries", m.Len())
	}
}

func TestMemorySweepsExpiredEntries(t *testing.T) {
	m, clock := newMemory(t, MemoryOptions{})
	for _, key := range []string{"a", "b", "c"} {
		_ = m.Set(t.Context(), key, []byte("x"), time.Minute)
	}
	_ = m.Set(t.Context(), "keep", []byte("x"), time.Hour)
	clock.Advance(2 * time.Minute)
	m.sweep()
	if m.Len() != 1 {
		t.Errorf("%d entries after a sweep, want 1", m.Len())
	}
}

func TestMemoryEvictsLeastRecentlyUsed(t *testing.T) {
	entry := int64(len("k1")+10) + entryOverhead
	m, _ := newMemory(t, MemoryOptions{MaxBytes: entry*3 + entry/2})
	ctx := t.Context()
	ten := []byte(strings.Repeat("x", 10))
	_ = m.Set(ctx, "k1", ten, 0)
	_ = m.Set(ctx, "k2", ten, 0)
	_ = m.Set(ctx, "k3", ten, 0)
	_, _, _ = m.Get(ctx, "k1") // k2 is now the least recently used
	_ = m.Set(ctx, "k4", ten, 0)
	if _, found, _ := m.Get(ctx, "k2"); found {
		t.Error("the least recently used entry was kept")
	}
	for _, key := range []string{"k1", "k3", "k4"} {
		if _, found, _ := m.Get(ctx, key); !found {
			t.Errorf("%s was evicted", key)
		}
	}
	// A value that cannot fit is not stored, and does not push everything else out.
	_ = m.Set(ctx, "huge", []byte(strings.Repeat("x", 10_000)), 0)
	if _, found, _ := m.Get(ctx, "huge"); found || m.Len() != 3 {
		t.Errorf("a huge value was stored: %v, %d entries", found, m.Len())
	}
}

func TestMemoryDeletePrefix(t *testing.T) {
	m, _ := newMemory(t, MemoryOptions{})
	ctx := t.Context()
	for _, key := range []string{"user:1", "user:2", "post:1"} {
		_ = m.Set(ctx, key, []byte("x"), 0)
	}
	_ = m.DeletePrefix(ctx, "user:")
	if m.Len() != 1 {
		t.Errorf("%d entries, want 1", m.Len())
	}
	if _, found, _ := m.Get(ctx, "post:1"); !found {
		t.Error("a key with another prefix was deleted")
	}
}

func TestMemoryOptionsAndClose(t *testing.T) {
	if _, err := NewMemory(MemoryOptions{MaxBytes: -1}); err == nil {
		t.Error("a negative size")
	}
	m, err := NewMemory(MemoryOptions{SweepInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Set(t.Context(), "k", []byte("v"), 0)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	if _, found, _ := m.Get(t.Context(), "k"); !found {
		t.Error("the entries are gone after Close")
	}
}

func TestMemoryRealSweeper(t *testing.T) {
	m, err := NewMemory(MemoryOptions{SweepInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_ = m.Set(t.Context(), "k", []byte("v"), 20*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for m.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Len() != 0 {
		t.Error("the sweeper did not remove an expired entry")
	}
}

func TestMemoryConcurrent(t *testing.T) {
	m, _ := newMemory(t, MemoryOptions{MaxBytes: 4096})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 500 {
				key := string(rune('a' + (i+j)%26))
				_ = m.Set(t.Context(), key, []byte(key), time.Minute)
				_, _, _ = m.Get(t.Context(), key)
				if j%50 == 0 {
					_ = m.DeletePrefix(t.Context(), "a")
				}
			}
		}()
	}
	wg.Wait()
}

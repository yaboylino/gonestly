package cache

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// MemoryOptions configures a memory store.
type MemoryOptions struct {
	// MaxBytes limits the size of the keys and values; the least recently used entries are
	// dropped to stay under it. 64 MiB when zero.
	MaxBytes int64
	// SweepInterval is how often expired entries that nobody asked for are removed; a minute
	// when zero.
	SweepInterval time.Duration
	// Now returns the time, for tests; time.Now when nil.
	Now func() time.Time
}

// entryOverhead is a rough cost of the bookkeeping of an entry, so many tiny entries count too.
const entryOverhead = 96

type memoryEntry struct {
	key     string
	value   []byte
	expires time.Time
}

func (e *memoryEntry) size() int64 { return int64(len(e.key)+len(e.value)) + entryOverhead }

// Memory is a Store in the memory of the process, with a size limit and expiry. Each instance of
// an application has its own, so use a shared store such as Redis when instances must agree.
type Memory struct {
	opts MemoryOptions

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front is the most recently used
	bytes   int64

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// NewMemory creates a memory store. It starts a goroutine that removes expired entries; call
// Close, or let the application stop it, to end that.
func NewMemory(opts MemoryOptions) (*Memory, error) {
	if opts.MaxBytes < 0 {
		return nil, errors.New("cache: MaxBytes cannot be negative")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 64 << 20
	}
	if opts.SweepInterval == 0 {
		opts.SweepInterval = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Memory{
		opts:    opts,
		entries: map[string]*list.Element{},
		order:   list.New(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go m.sweepLoop()
	return m, nil
}

// Get returns the value of a key that has not expired and marks it as recently used.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	element, ok := m.entries[key]
	if !ok {
		return nil, false, nil
	}
	entry := element.Value.(*memoryEntry) //nolint:forcetypeassert // the list only holds entries
	if m.expired(entry) {
		m.remove(element)
		return nil, false, nil
	}
	m.order.MoveToFront(element)
	return append([]byte(nil), entry.value...), true, nil
}

// Set stores a copy of the value. A value bigger than MaxBytes is not stored.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	entry := &memoryEntry{key: key, value: append([]byte(nil), value...)}
	if ttl > 0 {
		entry.expires = m.opts.Now().Add(ttl)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if element, ok := m.entries[key]; ok {
		m.remove(element)
	}
	if entry.size() > m.opts.MaxBytes {
		return nil
	}
	m.entries[key] = m.order.PushFront(entry)
	m.bytes += entry.size()
	for m.bytes > m.opts.MaxBytes {
		m.remove(m.order.Back())
	}
	return nil
}

// Delete removes keys.
func (m *Memory) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		if element, ok := m.entries[key]; ok {
			m.remove(element)
		}
	}
	return nil
}

// DeletePrefix removes every key that starts with prefix.
func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, element := range m.entries {
		if strings.HasPrefix(key, prefix) {
			m.remove(element)
		}
	}
	return nil
}

// Len returns the number of entries, including expired ones that were not removed yet.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (m *Memory) expired(e *memoryEntry) bool {
	return !e.expires.IsZero() && !m.opts.Now().Before(e.expires)
}

func (m *Memory) remove(element *list.Element) {
	entry := m.order.Remove(element).(*memoryEntry) //nolint:forcetypeassert // the list only holds entries
	delete(m.entries, entry.key)
	m.bytes -= entry.size()
}

func (m *Memory) sweepLoop() {
	defer close(m.done)
	ticker := time.NewTicker(m.opts.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.sweep()
		}
	}
}

func (m *Memory) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for element := m.order.Back(); element != nil; {
		previous := element.Prev()
		if m.expired(element.Value.(*memoryEntry)) { //nolint:forcetypeassert // the list only holds entries
			m.remove(element)
		}
		element = previous
	}
}

// Close stops the goroutine that removes expired entries. The entries stay readable.
func (m *Memory) Close() error {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	return nil
}

package cache_test

import (
	"testing"

	"github.com/yaboylino/gonestly/cache"
	"github.com/yaboylino/gonestly/cache/cachetest"
)

func TestMemoryMeetsTheStoreContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Store {
		m, err := cache.NewMemory(cache.MemoryOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		return m
	})
}

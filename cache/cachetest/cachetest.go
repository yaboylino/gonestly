// Package cachetest checks that a cache.Store keeps the contract that the cache relies on, for
// the authors of stores.
package cachetest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/cache"
)

// Run runs the contract against a store. newStore gives a store that no other test uses.
func Run(t *testing.T, newStore func(t *testing.T) cache.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("missing key", func(t *testing.T) {
		store := newStore(t)
		value, found, err := store.Get(ctx, "missing")
		if err != nil || found || value != nil {
			t.Errorf("Get = %q, %v, %v", value, found, err)
		}
	})

	t.Run("set and get", func(t *testing.T) {
		store := newStore(t)
		want := []byte("value with \x00 binary \xff bytes")
		if err := store.Set(ctx, "k", want, time.Minute); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.Get(ctx, "k")
		if err != nil || !found || !bytes.Equal(got, want) {
			t.Errorf("Get = %q, %v, %v", got, found, err)
		}
		if err := store.Set(ctx, "k", []byte("new"), time.Minute); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := store.Get(ctx, "k"); string(got) != "new" {
			t.Errorf("an overwritten key = %q", got)
		}
	})

	t.Run("empty value", func(t *testing.T) {
		store := newStore(t)
		if err := store.Set(ctx, "empty", []byte{}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.Get(ctx, "empty"); err != nil || !found {
			t.Errorf("an empty value: %v, %v", found, err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		store := newStore(t)
		_ = store.Set(ctx, "short", []byte("1"), 150*time.Millisecond)
		_ = store.Set(ctx, "forever", []byte("2"), 0)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, found, _ := store.Get(ctx, "short"); !found {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if _, found, _ := store.Get(ctx, "short"); found {
			t.Error("a key did not expire")
		}
		if _, found, _ := store.Get(ctx, "forever"); !found {
			t.Error("a key without ttl expired")
		}
	})

	t.Run("delete", func(t *testing.T) {
		store := newStore(t)
		_ = store.Set(ctx, "a", []byte("1"), time.Minute)
		_ = store.Set(ctx, "b", []byte("2"), time.Minute)
		_ = store.Set(ctx, "c", []byte("3"), time.Minute)
		if err := store.Delete(ctx, "a", "b", "never there"); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx); err != nil {
			t.Errorf("Delete without keys: %v", err)
		}
		for key, want := range map[string]bool{"a": false, "b": false, "c": true} {
			if _, found, _ := store.Get(ctx, key); found != want {
				t.Errorf("%s found = %v, want %v", key, found, want)
			}
		}
	})

	t.Run("delete by prefix", func(t *testing.T) {
		store := newStore(t)
		deleter, ok := store.(cache.PrefixDeleter)
		if !ok {
			t.Skip("the store cannot delete by prefix")
		}
		for _, key := range []string{"user:1", "user:2", "user*:3", "post:1"} {
			_ = store.Set(ctx, key, []byte("x"), time.Minute)
		}
		if err := deleter.DeletePrefix(ctx, "user:"); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]bool{"user:1": false, "user:2": false, "user*:3": true, "post:1": true} {
			if _, found, _ := store.Get(ctx, key); found != want {
				t.Errorf("%s found = %v, want %v", key, found, want)
			}
		}
		// Characters that mean something to a pattern are taken literally.
		_ = store.Set(ctx, "a*b", []byte("x"), time.Minute)
		_ = store.Set(ctx, "axb", []byte("x"), time.Minute)
		if err := deleter.DeletePrefix(ctx, "a*"); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := store.Get(ctx, "a*b"); found {
			t.Error("a key with the literal prefix survived")
		}
		if _, found, _ := store.Get(ctx, "axb"); !found {
			t.Error("a key matching the prefix as a pattern was deleted")
		}
	})

	t.Run("many keys", func(t *testing.T) {
		store := newStore(t)
		for i := range 300 {
			_ = store.Set(ctx, "bulk:"+string(rune('a'+i%26))+string(rune('A'+i/26)), []byte("x"), time.Minute)
		}
		if deleter, ok := store.(cache.PrefixDeleter); ok {
			if err := deleter.DeletePrefix(ctx, "bulk:"); err != nil {
				t.Fatal(err)
			}
			if _, found, _ := store.Get(ctx, "bulk:aA"); found {
				t.Error("a key survived a prefix delete of many")
			}
		}
	})
}

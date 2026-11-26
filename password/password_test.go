package password

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func fast(t *testing.T, opts Options) *Hasher {
	t.Helper()
	if opts.Memory == 0 {
		opts.Memory = 64
	}
	if opts.Iterations == 0 {
		opts.Iterations = 1
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = 1
	}
	h, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashAndVerify(t *testing.T) {
	h := fast(t, Options{})
	hash, err := h.Hash(t.Context(), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("hash = %s", hash)
	}
	ok, upgrade, err := h.Verify(t.Context(), "correct horse battery staple", hash)
	if err != nil || !ok || upgrade {
		t.Errorf("right password: ok=%v upgrade=%v err=%v", ok, upgrade, err)
	}
	ok, _, err = h.Verify(t.Context(), "wrong", hash)
	if err != nil || ok {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}
	other, _ := h.Hash(t.Context(), "correct horse battery staple")
	if other == hash {
		t.Error("two hashes of one password are equal, so the salt is not random")
	}
	// The empty password is hashed like any other; a policy of minimum length is for the application.
	empty, err := h.Hash(t.Context(), "")
	if ok, _, verr := h.Verify(t.Context(), "", empty); err != nil || verr != nil || !ok {
		t.Errorf("the empty password: %v %v %v", err, verr, ok)
	}
}

func TestVerifiesAHashFromAnotherImplementation(t *testing.T) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte("secret"), salt, 2, 32, 2, 32)
	encoded := encode(32, 2, 2, salt, key)
	ok, upgrade, err := fast(t, Options{Memory: 32, Iterations: 2, Parallelism: 2}).Verify(t.Context(), "secret", encoded)
	if err != nil || !ok || upgrade {
		t.Errorf("ok=%v upgrade=%v err=%v", ok, upgrade, err)
	}
}

func TestUpgrade(t *testing.T) {
	old := fast(t, Options{Memory: 64, Iterations: 1, Parallelism: 1})
	hash, _ := old.Hash(t.Context(), "secret")
	for name, stronger := range map[string]Options{
		"memory":      {Memory: 128, Iterations: 1, Parallelism: 1},
		"iterations":  {Memory: 64, Iterations: 2, Parallelism: 1},
		"parallelism": {Memory: 64, Iterations: 1, Parallelism: 2},
	} {
		ok, upgrade, err := fast(t, stronger).Verify(t.Context(), "secret", hash)
		if err != nil || !ok || !upgrade {
			t.Errorf("%s: ok=%v upgrade=%v err=%v", name, ok, upgrade, err)
		}
	}
	// A wrong password never asks for an upgrade, and a weaker setting does not either.
	if _, upgrade, _ := fast(t, Options{Memory: 128}).Verify(t.Context(), "wrong", hash); upgrade {
		t.Error("a wrong password asked for an upgrade")
	}
	strong, _ := fast(t, Options{Memory: 128}).Hash(t.Context(), "secret")
	if _, upgrade, _ := old.Verify(t.Context(), "secret", strong); upgrade {
		t.Error("a stronger hash asked for an upgrade")
	}
}

func TestPepper(t *testing.T) {
	peppered := fast(t, Options{Pepper: []byte("pepper")})
	hash, _ := peppered.Hash(t.Context(), "secret")
	if ok, _, _ := peppered.Verify(t.Context(), "secret", hash); !ok {
		t.Error("the right password failed with the pepper")
	}
	if ok, _, _ := fast(t, Options{Pepper: []byte("other")}).Verify(t.Context(), "secret", hash); ok {
		t.Error("another pepper verified the hash")
	}
	if ok, _, _ := fast(t, Options{}).Verify(t.Context(), "secret", hash); ok {
		t.Error("no pepper verified the hash")
	}
}

func TestInvalidHashes(t *testing.T) {
	h := fast(t, Options{})
	good, _ := h.Hash(t.Context(), "x")
	parts := strings.Split(good, "$")
	replace := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}
	for name, encoded := range map[string]string{
		"empty":           "",
		"bcrypt":          "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":         replace(1, "argon2i"),
		"old version":     replace(2, "v=16"),
		"missing params":  replace(3, "m=64,t=1"),
		"extra param":     replace(3, "m=64,t=1,p=1,x=1"),
		"not a number":    replace(3, "m=lots,t=1,p=1"),
		"zero":            replace(3, "m=64,t=0,p=1"),
		"huge memory":     replace(3, "m=4000000,t=1,p=1"),
		"too little":      replace(3, "m=7,t=1,p=1"),
		"huge iterations": replace(3, "m=64,t=100000,p=1"),
		"bad salt":        replace(4, "!!!"),
		"short salt":      replace(4, "AAAA"),
		"bad key":         replace(5, "!!!"),
		"short key":       replace(5, "AAAA"),
		"too many parts":  good + "$x",
	} {
		if _, _, err := h.Verify(t.Context(), "x", encoded); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTooLong(t *testing.T) {
	h := fast(t, Options{})
	long := strings.Repeat("a", MaxLength+1)
	if _, err := h.Hash(t.Context(), long); !errors.Is(err, ErrTooLong) {
		t.Errorf("Hash: %v", err)
	}
	if _, _, err := h.Verify(t.Context(), long, "x"); !errors.Is(err, ErrTooLong) {
		t.Errorf("Verify: %v", err)
	}
	if err := h.Dummy(t.Context(), long); !errors.Is(err, ErrTooLong) {
		t.Errorf("Dummy: %v", err)
	}
	if _, err := h.Hash(t.Context(), strings.Repeat("a", MaxLength)); err != nil {
		t.Errorf("a password of exactly MaxLength: %v", err)
	}
}

func TestDummy(t *testing.T) {
	if err := fast(t, Options{}).Dummy(t.Context(), "anything"); err != nil {
		t.Fatal(err)
	}
}

func TestWaitsForASlotAndGivesUpWithTheContext(t *testing.T) {
	h := fast(t, Options{MaxConcurrent: 1})
	h.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Hash: %v", err)
	}
	if _, _, err := h.Verify(ctx, "x", "$argon2id$v=19$m=64,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Verify: %v", err)
	}
	if err := h.Dummy(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Dummy: %v", err)
	}
	h.release()
	if _, err := h.Hash(t.Context(), "x"); err != nil {
		t.Errorf("after the slot was free: %v", err)
	}
}

func TestOptionsAreChecked(t *testing.T) {
	for name, opts := range map[string]Options{
		"negative concurrency": {MaxConcurrent: -1},
		"too little memory":    {Memory: 7, Parallelism: 1},
		"too much memory":      {Memory: maxStoredMemory + 1},
		"too many iterations":  {Iterations: maxStoredIterations + 1},
		"too many lanes":       {Memory: 1 << 16, Parallelism: maxStoredParallelism + 1},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	h, err := New(Options{})
	if err != nil || h.opts.Memory != 64*1024 || h.opts.Iterations != 3 || h.opts.Parallelism != 4 || cap(h.slots) < 1 {
		t.Errorf("defaults = %+v, %v", h, err)
	}
}

func TestDefaultHashIsRealistic(t *testing.T) {
	if testing.Short() {
		t.Skip("uses 64 MiB")
	}
	h, _ := New(Options{})
	hash, err := h.Hash(t.Context(), "secret")
	if err != nil || !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash = %s, err = %v", hash, err)
	}
}

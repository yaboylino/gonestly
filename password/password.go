package password

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Options configures a Hasher. The zero value uses the low memory recommendation of RFC 9106:
// 64 MiB, 3 passes and 4 lanes, which takes tens of milliseconds on a server.
type Options struct {
	// Memory is the memory of one hash in KiB; 65536 (64 MiB) when zero.
	Memory uint32
	// Iterations is the number of passes over the memory; 3 when zero.
	Iterations uint32
	// Parallelism is the number of lanes; 4 when zero.
	Parallelism uint8
	// MaxConcurrent limits how many hashes are computed at the same time, because each takes
	// Memory; the number of CPUs when zero. Others wait, until their context ends.
	MaxConcurrent int
	// Pepper is a secret that is mixed into every password before hashing. Keep it out of the
	// database, for example in the environment. Changing it makes every stored hash fail.
	Pepper []byte
}

const (
	saltLength = 16
	keyLength  = 32
	// MaxLength is the longest password in bytes that Hash and Verify take. Longer ones are
	// refused, so a request cannot make the server hash megabytes.
	MaxLength = 1024

	defaultMemory      = 64 * 1024
	defaultIterations  = 3
	defaultParallelism = 4

	// Limits on the parameters in a stored hash, so a corrupted value cannot ask for gigabytes.
	maxStoredMemory      = 1 << 20
	maxStoredIterations  = 64
	maxStoredParallelism = 64
)

// Errors that Hash and Verify return.
var (
	// ErrTooLong is returned for a password longer than MaxLength bytes.
	ErrTooLong = errors.New("password: the password is too long")
	// ErrInvalidHash is returned when a stored hash is not an Argon2id hash in the PHC format.
	ErrInvalidHash = errors.New("password: the stored hash is not a valid Argon2id hash")
)

// Hasher hashes and checks passwords. It is safe for concurrent use.
type Hasher struct {
	opts  Options
	slots chan struct{}
}

// New creates a Hasher and checks the options.
func New(opts Options) (*Hasher, error) {
	if opts.Memory == 0 {
		opts.Memory = defaultMemory
	}
	if opts.Iterations == 0 {
		opts.Iterations = defaultIterations
	}
	if opts.Parallelism == 0 {
		opts.Parallelism = defaultParallelism
	}
	if opts.MaxConcurrent < 0 {
		return nil, errors.New("password: MaxConcurrent cannot be negative")
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = runtime.NumCPU()
	}
	switch {
	case opts.Memory < 8*uint32(opts.Parallelism):
		return nil, fmt.Errorf("password: Memory must be at least 8 KiB per lane, %d KiB for %d lanes", 8*uint32(opts.Parallelism), opts.Parallelism)
	case opts.Memory > maxStoredMemory:
		return nil, fmt.Errorf("password: Memory above %d KiB is not supported", maxStoredMemory)
	case opts.Iterations > maxStoredIterations:
		return nil, fmt.Errorf("password: Iterations above %d is not supported", maxStoredIterations)
	case opts.Parallelism > maxStoredParallelism:
		return nil, fmt.Errorf("password: Parallelism above %d is not supported", maxStoredParallelism)
	}
	opts.Pepper = append([]byte(nil), opts.Pepper...)
	return &Hasher{opts: opts, slots: make(chan struct{}, opts.MaxConcurrent)}, nil
}

// acquire waits for a free slot, or for the context to end.
func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// prepare mixes in the pepper: an HMAC of the password, which also makes every password the
// same length.
func (h *Hasher) prepare(password string) []byte {
	if len(h.opts.Pepper) == 0 {
		return []byte(password)
	}
	mac := hmac.New(sha256.New, h.opts.Pepper)
	mac.Write([]byte(password))
	return mac.Sum(nil)
}

// Hash returns the Argon2id hash of the password in the PHC format, with a new random salt.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if len(password) > MaxLength {
		return "", ErrTooLong
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: reading random bytes: %w", err)
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey(h.prepare(password), salt, h.opts.Iterations, h.opts.Memory, h.opts.Parallelism, keyLength)
	return encode(h.opts.Memory, h.opts.Iterations, h.opts.Parallelism, salt, key), nil
}

// Verify checks a password against a stored hash. A wrong password is not an error: ok is
// false. An error means that the check could not be done, such as a hash that is not valid.
//
// upgrade is true when the password is right but the hash was made with weaker settings than
// the current ones; hash the password again and store the result.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, upgrade bool, err error) {
	if len(password) > MaxLength {
		return false, false, ErrTooLong
	}
	memory, iterations, parallelism, salt, key, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	other := argon2.IDKey(h.prepare(password), salt, iterations, memory, parallelism, uint32(len(key))) //nolint:gosec // the length was decoded from a hash of at most 1 KiB
	if subtle.ConstantTimeCompare(key, other) != 1 {
		return false, false, nil
	}
	upgrade = memory < h.opts.Memory || iterations < h.opts.Iterations || parallelism < h.opts.Parallelism || len(key) < keyLength
	return true, upgrade, nil
}

// Dummy does the work of a check without a hash to compare with. Call it when the user does
// not exist, so that the time of a login does not tell whether the account exists.
func (h *Hasher) Dummy(ctx context.Context, password string) error {
	if len(password) > MaxLength {
		return ErrTooLong
	}
	if err := h.acquire(ctx); err != nil {
		return err
	}
	defer h.release()
	argon2.IDKey(h.prepare(password), make([]byte, saltLength), h.opts.Iterations, h.opts.Memory, h.opts.Parallelism, keyLength)
	return nil
}

func encode(memory, iterations uint32, parallelism uint8, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, iterations, parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decode(encoded string) (memory, iterations uint32, parallelism uint8, salt, key []byte, err error) {
	invalid := func(why string) error { return fmt.Errorf("%w: %s", ErrInvalidHash, why) }
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, invalid("expected $argon2id$v=19$m=..,t=..,p=..$salt$hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return 0, 0, 0, nil, nil, invalid("unsupported version " + parts[2])
	}
	settings := map[string]uint64{}
	for _, field := range strings.Split(parts[3], ",") {
		name, value, ok := strings.Cut(field, "=")
		n, parseErr := strconv.ParseUint(value, 10, 32)
		if !ok || parseErr != nil {
			return 0, 0, 0, nil, nil, invalid("bad parameter " + field)
		}
		settings[name] = n
	}
	m, t, p := settings["m"], settings["t"], settings["p"]
	switch {
	case m == 0 || t == 0 || p == 0 || len(settings) != 3:
		return 0, 0, 0, nil, nil, invalid("m, t and p are required")
	case m > maxStoredMemory || t > maxStoredIterations || p > maxStoredParallelism || m < 8*p:
		return 0, 0, 0, nil, nil, invalid("parameters out of range")
	}
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil || len(salt) < 8 || len(salt) > 64 {
		return 0, 0, 0, nil, nil, invalid("bad salt")
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) < 16 || len(key) > 1024 {
		return 0, 0, 0, nil, nil, invalid("bad hash")
	}
	return uint32(m), uint32(t), uint8(p), salt, key, nil
}

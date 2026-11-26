package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yaboylino/gonestly"
)

// ResponseOption changes how Responses builds its keys.
type ResponseOption func(*responseCache)

type responseCache struct {
	cache  *Cache
	ttl    time.Duration
	vary   []string
	key    func(ctx context.Context, r *http.Request) string
	shared bool
}

// Vary adds request headers to the key, for responses that depend on them, such as
// Accept-Language.
func Vary(headers ...string) ResponseOption {
	return func(rc *responseCache) {
		for _, h := range headers {
			rc.vary = append(rc.vary, http.CanonicalHeaderKey(h))
		}
	}
}

// KeyFunc decides the key of a request yourself, for example by the user or the tenant. An empty
// key lets the request through without the cache. The path and query are always part of the key.
func KeyFunc(fn func(ctx context.Context, r *http.Request) string) ResponseOption {
	return func(rc *responseCache) { rc.key = fn }
}

// SharedAcrossUsers gives every caller the same cached response, whatever Authorization header or
// cookies they send. By default each credential has a response of its own, which is safe for
// data that depends on who asks. Use this for data that is the same for everybody who is allowed
// to see it, after the guards have let them in.
func SharedAcrossUsers() ResponseOption {
	return func(rc *responseCache) { rc.shared = true }
}

// ResponsePrefix returns the start of the keys of the cached responses of a path and everything
// below it, for DeletePrefix after the data behind it changed:
//
//	c.DeletePrefix(ctx, cache.ResponsePrefix("/products"))
func ResponsePrefix(path string) string { return "http:GET:" + path }

const (
	// maxKeyLength is where a key is shortened to a hash, so that stores with a limit accept it.
	maxKeyLength = 400
	cacheHeader  = "X-Cache"
)

// Responses is an interceptor that caches the response of a GET route for ttl. It runs after the
// guards, binding and validation, so a response is only served to a caller that was allowed in
// and sent a valid request.
//
//	gonestly.Get(r, "/products", c.List, gonestly.WithInterceptors(cache.Responses(store, time.Minute)))
//
// The key is made of the path, the query and, unless SharedAcrossUsers is set, the credentials of
// the caller. Responses of errors, of other methods and without a body are not cached. When many
// requests miss at the same moment the handler runs once and they share the result. Every
// response says X-Cache: HIT or MISS. Invalidate with DeletePrefix and ResponsePrefix.
func Responses(c *Cache, ttl time.Duration, opts ...ResponseOption) gonestly.Interceptor {
	rc := &responseCache{cache: c, ttl: ttl}
	for _, opt := range opts {
		opt(rc)
	}
	return rc
}

type cached struct{ data json.RawMessage }

type uncacheable struct{ result any }

// Intercept implements gonestly.Interceptor.
func (rc *responseCache) Intercept(ctx context.Context, r *http.Request, next gonestly.Next) (any, error) {
	if r.Method != http.MethodGet {
		return next(ctx)
	}
	key := rc.requestKey(ctx, r)
	if key == "" {
		return next(ctx)
	}
	header := gonestly.ResponseHeader(ctx)
	if raw, found := Get[json.RawMessage](ctx, rc.cache, key); found {
		header.Set(cacheHeader, "HIT")
		return raw, nil
	}
	header.Set(cacheHeader, "MISS")
	value, err := rc.cache.flight.do(ctx, rc.cache.key(key), func() (any, error) {
		loadCtx := context.WithoutCancel(ctx)
		rc.cache.loads.Add(1)
		result, err := next(loadCtx)
		if err != nil {
			return nil, err
		}
		if _, empty := result.(gonestly.Empty); empty || result == nil {
			return uncacheable{result}, nil
		}
		data, err := json.Marshal(result)
		if err != nil {
			return uncacheable{result}, nil //nolint:nilerr // the server reports an unencodable result itself
		}
		if err := Set(loadCtx, rc.cache, key, json.RawMessage(data), rc.ttl); err != nil {
			return uncacheable{result}, nil //nolint:nilerr // see above
		}
		return cached{data}, nil
	})
	if err != nil {
		return nil, err
	}
	switch v := value.(type) {
	case cached:
		return v.data, nil
	case uncacheable:
		return v.result, nil
	default:
		return value, nil
	}
}

// requestKey builds the key of a request, or "" when it must not be cached.
func (rc *responseCache) requestKey(ctx context.Context, r *http.Request) string {
	var b strings.Builder
	b.WriteString(ResponsePrefix(r.URL.Path))
	if query := r.URL.Query().Encode(); query != "" {
		b.WriteString("?" + query)
	}
	var identity strings.Builder
	if rc.key != nil {
		custom := rc.key(ctx, r)
		if custom == "" {
			return ""
		}
		identity.WriteString("key=" + custom + "\n")
	} else if !rc.shared {
		for _, name := range []string{"Authorization", "Cookie"} {
			if value := r.Header.Get(name); value != "" {
				identity.WriteString(name + "=" + value + "\n")
			}
		}
	}
	for _, name := range rc.vary {
		identity.WriteString(name + "=" + strings.Join(r.Header.Values(name), ",") + "\n")
	}
	if identity.Len() > 0 {
		sum := sha256.Sum256([]byte(identity.String()))
		b.WriteString("|" + hex.EncodeToString(sum[:8]))
	}
	key := b.String()
	if len(key) > maxKeyLength {
		sum := sha256.Sum256([]byte(key))
		key = ResponsePrefix(r.URL.Path)
		if len(key) > maxKeyLength-80 {
			key = key[:maxKeyLength-80]
		}
		key += "#" + hex.EncodeToString(sum[:16])
	}
	return key
}

// maxETagBody is the largest response that ETag buffers to hash.
const maxETagBody = 1 << 20

// ETag is middleware that gives GET and HEAD responses an ETag, a fingerprint of the body, and
// answers 304 Not Modified, without the body, to a request whose If-None-Match names that
// fingerprint. Clients and CDNs then revalidate for the price of a header. A response that
// already has an ETag keeps it, and a response over 1 MiB, or one that is flushed while it is
// written, passes untouched.
func ETag() gonestly.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			ew := &etagWriter{ResponseWriter: w}
			next.ServeHTTP(ew, r)
			ew.finish(r)
		})
	}
}

type etagWriter struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	passthrough bool
}

func (e *etagWriter) WriteHeader(status int) {
	if e.passthrough {
		e.ResponseWriter.WriteHeader(status)
		return
	}
	if e.status == 0 {
		e.status = status
	}
}

func (e *etagWriter) Write(p []byte) (int, error) {
	if e.passthrough {
		return e.ResponseWriter.Write(p)
	}
	if e.status == 0 {
		e.status = http.StatusOK
	}
	if e.body.Len()+len(p) > maxETagBody {
		e.release()
		return e.ResponseWriter.Write(p)
	}
	return e.body.Write(p)
}

// Flush gives up on buffering: the handler is streaming.
func (e *etagWriter) Flush() {
	e.release()
	if f, ok := e.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (e *etagWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// release sends what was held back, and from now on passes everything through.
func (e *etagWriter) release() {
	if e.passthrough {
		return
	}
	e.passthrough = true
	if e.status != 0 {
		e.ResponseWriter.WriteHeader(e.status)
	}
	_, _ = e.ResponseWriter.Write(e.body.Bytes())
	e.body.Reset()
}

func (e *etagWriter) finish(r *http.Request) {
	if e.passthrough {
		return
	}
	header := e.Header()
	status := e.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK && header.Get("ETag") == "" && e.body.Len() > 0 {
		sum := sha256.Sum256(e.body.Bytes())
		header.Set("ETag", `"`+base64.RawURLEncoding.EncodeToString(sum[:12])+`"`)
	}
	if status == http.StatusOK && matches(r.Header.Values("If-None-Match"), header.Get("ETag")) {
		header.Del("Content-Length")
		header.Del("Content-Type")
		header.Del("Content-Encoding")
		e.ResponseWriter.WriteHeader(http.StatusNotModified)
		return
	}
	e.ResponseWriter.WriteHeader(status)
	_, _ = e.ResponseWriter.Write(e.body.Bytes())
}

// matches reports whether an If-None-Match header names the tag, comparing weakly as RFC 9110
// asks for this header.
func matches(values []string, tag string) bool {
	if tag == "" {
		return false
	}
	tag = strings.TrimPrefix(tag, "W/")
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == tag {
				return true
			}
		}
	}
	return false
}

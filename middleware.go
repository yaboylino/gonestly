package gonestly

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"net/http"
	"path"
	"time"
)

// RequestIDHeader carries the ID of a request, in the request and in the response.
const RequestIDHeader = "X-Request-ID"

const maxRequestIDLength = 128

type requestIDKey struct{}

// RequestID is middleware that gives every request an ID: the X-Request-ID header a proxy or
// client sent, or a new random one. The ID is returned in the response header, added to the
// logs of the application about that request and available through RequestIDFrom.
//
// Add it to ServerConfig.Middleware before AccessLog, so the access log can show the ID.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validRequestID(id) {
				id = rand.Text()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
		})
	}
}

// RequestIDFrom returns the ID that RequestID gave the request of ctx, or "" without one.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts an ID from outside only when it is safe to log and to send back.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for _, r := range id {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !letter && r != '-' && r != '_' && r != '.' && r != ':' {
			return false
		}
	}
	return true
}

// requestAttrs are the log attributes that identify a request. The response header also
// carries the ID, for code that runs outside the RequestID middleware.
func requestAttrs(w http.ResponseWriter, r *http.Request) []any {
	attrs := []any{slog.String("method", r.Method), slog.String("path", r.URL.Path)}
	id := requestIDOf(r.Context())
	if id == "" {
		id = w.Header().Get(RequestIDHeader)
	}
	if id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	return attrs
}

// AccessLogOption changes what AccessLog logs.
type AccessLogOption func(*accessLog)

type accessLog struct {
	skip []string
	slow time.Duration
}

// SkipPaths leaves the requests for these paths out of the access log, such as the health
// checks that a load balancer calls every few seconds. A path can use * as in path.Match:
// "/health/*".
func SkipPaths(patterns ...string) AccessLogOption {
	return func(a *accessLog) { a.skip = append(a.skip, patterns...) }
}

// SlowAfter logs a request that takes longer than d at warn level, with slow=true, so slow
// requests stand out and can be alerted on.
func SlowAfter(d time.Duration) AccessLogOption {
	return func(a *accessLog) { a.slow = d }
}

func (a *accessLog) skipped(p string) bool {
	for _, pattern := range a.skip {
		if ok, _ := path.Match(pattern, p); ok || pattern == p {
			return true
		}
	}
	return false
}

// AccessLog is middleware that logs every request when it has been answered: method, path, the
// route that handled it, status, size, duration, the address of the client and its user agent,
// and the request ID, plus whatever code added with LogAttrs. It logs at error level for a 5xx
// status, at warn level for a slow request (see SlowAfter) and at info level otherwise.
//
// The address is the one of the connection, or of the client when TrustProxies is used.
func AccessLog(logger *slog.Logger, opts ...AccessLogOption) Middleware {
	var config accessLog
	for _, opt := range opts {
		opt(&config)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if config.skipped(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w}
			defer func() {
				// A panic passes through to be answered with a 500 further out.
				v := recover()
				status := recorder.status
				switch {
				case v != nil:
					status = http.StatusInternalServerError
				case status == 0:
					status = http.StatusOK
				}
				took := time.Since(start)
				level := slog.LevelInfo
				attrs := requestAttrs(w, r)
				attrs = append(attrs,
					slog.Int("status", status),
					slog.Int64("bytes", recorder.bytes),
					slog.Duration("duration", took),
				)
				if f := logFieldsFrom(r.Context()); f != nil {
					if _, route, _ := f.snapshot(); route != "" {
						attrs = append(attrs, slog.String("route", route))
					}
				}
				remote := remoteHost(r)
				if f := logFieldsFrom(r.Context()); f != nil {
					f.mu.Lock()
					if f.clientIP != "" {
						remote = f.clientIP
					}
					f.mu.Unlock()
				}
				attrs = append(attrs, slog.String("remote_addr", remote))
				if agent := r.UserAgent(); agent != "" {
					attrs = append(attrs, slog.String("user_agent", agent))
				}
				switch {
				case status >= http.StatusInternalServerError:
					level = slog.LevelError
				case config.slow > 0 && took > config.slow:
					level = slog.LevelWarn
					attrs = append(attrs, slog.Bool("slow", true))
				}
				logger.Log(r.Context(), level, "request", attrs...)
				if v != nil {
					panic(v)
				}
			}()
			next.ServeHTTP(recorder, r)
		})
	}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// statusRecorder remembers the status and size of a response.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Flush supports streaming handlers.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

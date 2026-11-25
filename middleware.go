package gonestly

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
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
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
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
	id := RequestIDFrom(r.Context())
	if id == "" {
		id = w.Header().Get(RequestIDHeader)
	}
	if id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	return attrs
}

// AccessLog is middleware that logs every request when it has been answered, with its
// method, path, status, size, duration and request ID: at error level for a 5xx status and
// at info level otherwise.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				level := slog.LevelInfo
				if status >= http.StatusInternalServerError {
					level = slog.LevelError
				}
				attrs := append(requestAttrs(w, r),
					slog.Int("status", status),
					slog.Int64("bytes", recorder.bytes),
					slog.Duration("duration", time.Since(start)),
				)
				logger.Log(r.Context(), level, "request", attrs...)
				if v != nil {
					panic(v)
				}
			}()
			next.ServeHTTP(recorder, r)
		})
	}
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

package gonestly

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// logFields are the details of the request in progress that every log line about it carries.
// The server creates one per request before any middleware runs, so code further in can add to
// what the access log, which wraps it, writes in the end.
type logFields struct {
	mu        sync.Mutex
	requestID string
	route     string
	clientIP  string
	attrs     []slog.Attr
}

type logFieldsKey struct{}

func withLogFields(ctx context.Context) (context.Context, *logFields) {
	f := &logFields{}
	return context.WithValue(ctx, logFieldsKey{}, f), f
}

func logFieldsFrom(ctx context.Context) *logFields {
	f, _ := ctx.Value(logFieldsKey{}).(*logFields)
	return f
}

func (f *logFields) snapshot() (requestID, route string, attrs []slog.Attr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requestID, f.route, append([]slog.Attr(nil), f.attrs...)
}

// LogAttrs adds attributes to every log line about the request of ctx from now on, including
// the access log line that is written when the request ends. Use it to name who is calling,
// for example in a guard:
//
//	gonestly.LogAttrs(ctx, slog.String("user_id", claims.Subject))
//
// It does nothing for a context that does not belong to a request.
func LogAttrs(ctx context.Context, attrs ...slog.Attr) {
	f := logFieldsFrom(ctx)
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attrs = append(f.attrs, attrs...)
}

// WithRequestID returns a context that carries a request ID, for work that continues a request
// elsewhere, such as a background job that was enqueued by one.
func WithRequestID(ctx context.Context, id string) context.Context {
	if f := logFieldsFrom(ctx); f != nil {
		f.mu.Lock()
		f.requestID = id
		f.mu.Unlock()
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDOf finds the ID of the request of ctx: in the context, or in the fields that the
// server shares with the middleware outside RequestID.
func requestIDOf(ctx context.Context) string {
	if id := RequestIDFrom(ctx); id != "" {
		return id
	}
	if f := logFieldsFrom(ctx); f != nil {
		id, _, _ := f.snapshot()
		return id
	}
	return ""
}

// LogHandler wraps a handler so that every record logged with a context, such as
// logger.InfoContext(ctx, ...), gets the request ID and the attributes added with LogAttrs of the
// request that the context belongs to. The application wraps its logger like this, so the
// logger that providers receive does it already.
func LogHandler(next slog.Handler) slog.Handler {
	if h, ok := next.(*contextHandler); ok {
		return h
	}
	return &contextHandler{next: next}
}

type contextHandler struct{ next slog.Handler }

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	var attrs []slog.Attr
	if id := requestIDOf(ctx); id != "" && !hasAttr(r, "request_id") {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if f := logFieldsFrom(ctx); f != nil {
		_, _, extra := f.snapshot()
		for _, a := range extra {
			if !hasAttr(r, a.Key) {
				attrs = append(attrs, a)
			}
		}
	}
	if len(attrs) > 0 {
		r = r.Clone()
		r.AddAttrs(attrs...)
	}
	return h.next.Handle(ctx, r)
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(name)}
}

// DevEnv is set by `gonestly dev` for the application it runs, so that NewLogger writes
// readable text there even though the output goes through a pipe.
const DevEnv = "GONESTLY_DEV"

// LogOptions configure NewLogger. The zero value logs info and above to standard error, as text
// in a terminal and as JSON elsewhere.
type LogOptions struct {
	// Level is "debug", "info", "warn" or "error"; "info" when empty.
	Level string
	// Format is "text", "json" or "auto"; "auto" when empty writes text to a terminal and under
	// `gonestly dev`, and JSON to anything else, such as a log collector.
	Format string
	// Output is where the logs go; standard error when nil.
	Output io.Writer
}

// NewLogger builds the logger of an application from settings that usually come from the
// environment, such as LOG_LEVEL and LOG_FORMAT. Pass it to WithLogger.
func NewLogger(opts LogOptions) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(opts.Level)) {
	case "", "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("gonestly: the log level %q is not debug, info, warn or error", opts.Level)
	}
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" || format == "auto" {
		format = "json"
		if os.Getenv(DevEnv) != "" {
			format = "text"
		} else if f, ok := out.(*os.File); ok {
			if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				format = "text"
			}
		}
	}
	handlerOptions := &slog.HandlerOptions{Level: level}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(out, handlerOptions)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(out, handlerOptions)), nil
	default:
		return nil, fmt.Errorf("gonestly: the log format %q is not text, json or auto", opts.Format)
	}
}

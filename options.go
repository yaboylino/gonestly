package gonestly

import (
	"log/slog"
	"time"
)

const defaultShutdownTimeout = 10 * time.Second

// Option configures an App.
type Option func(*options)

type options struct {
	logger          *slog.Logger
	shutdownTimeout time.Duration
	shutdownDelay   time.Duration
	overrides       []Provider
	listen          bool
}

// WithLogger sets the logger the application logs to and provides to every module; see
// NewLogger. It defaults to slog.Default(). The application wraps it with LogHandler, so a
// line logged with a request's context carries the request ID.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		if logger != nil {
			o.logger = logger
		}
	}
}

// WithShutdownTimeout sets how long App.Run waits for providers to stop. It defaults to 10 seconds.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(o *options) {
		if timeout > 0 {
			o.shutdownTimeout = timeout
		}
	}
}

// WithShutdownDelay makes App.Run wait before it stops the application. During the delay the
// server still answers, but Health reports that it is unavailable, which gives a load balancer
// or an orchestrator the time to send its traffic elsewhere. It defaults to no delay.
func WithShutdownDelay(delay time.Duration) Option {
	return func(o *options) {
		if delay > 0 {
			o.shutdownDelay = delay
		}
	}
}

// Override replaces the provider of the same type in whichever module declares it.
// It is meant for tests, for example to swap a repository for an in-memory fake.
func Override(items ...any) Option {
	return func(o *options) {
		o.overrides = append(o.overrides, Providers(items...)...)
	}
}

// WithoutListener builds the HTTP handler without listening on a port. It is meant for tests
// that send requests to App.Handler; see the gonestlytest package.
func WithoutListener() Option {
	return func(o *options) { o.listen = false }
}

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
	overrides       []Provider
}

// WithLogger sets the logger the application logs to and provides to every module.
// It defaults to slog.Default().
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

// Override replaces the provider of the same type in whichever module declares it.
// It is meant for tests, for example to swap a repository for an in-memory fake.
func Override(items ...any) Option {
	return func(o *options) {
		o.overrides = append(o.overrides, Providers(items...)...)
	}
}

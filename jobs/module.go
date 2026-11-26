package jobs

import (
	"log/slog"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"
)

// Module returns a global module that provides the *Queue to every module. Import it once, in
// the root module, after the sql module. The jobs table must exist: see Migration.
func Module(opts Options) *gonestly.Module {
	return newModule(func(db *sql.DB, logger *slog.Logger) (*Queue, error) {
		return New(db, withLogger(opts, logger))
	})
}

// ModuleFunc is like Module, but builds the options from a provider of a global module,
// typically the configuration from config.Module.
func ModuleFunc[C any](options func(C) Options) *gonestly.Module {
	return newModule(func(c C, db *sql.DB, logger *slog.Logger) (*Queue, error) {
		return New(db, withLogger(options(c), logger))
	})
}

func withLogger(opts Options, logger *slog.Logger) Options {
	if opts.Logger == nil {
		opts.Logger = logger
	}
	return opts
}

func newModule(constructor any) *gonestly.Module {
	return &gonestly.Module{
		Name:      "jobs",
		Global:    true,
		Providers: gonestly.Providers(constructor),
		Exports:   gonestly.Exports(gonestly.Type[*Queue]()),
	}
}

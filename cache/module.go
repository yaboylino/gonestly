package cache

import (
	"log/slog"

	"github.com/yaboylino/gonestly"
)

// Module returns a global module that provides the *Cache to every module. Import it once, in
// the root module. The application stops the store when it stops.
func Module(opts Options) *gonestly.Module {
	return newModule(func(logger *slog.Logger) (*Cache, error) { return New(withLogger(opts, logger)) })
}

// ModuleFunc is like Module, but builds the options from a provider of a global module,
// typically the configuration from config.Module, for example to choose a shared store:
//
//	cache.ModuleFunc(func(cfg Config) cache.Options {
//		return cache.Options{Store: redis.NewStore(client), Prefix: "shop:"}
//	})
func ModuleFunc[C any](options func(C) Options) *gonestly.Module {
	return newModule(func(c C, logger *slog.Logger) (*Cache, error) { return New(withLogger(options(c), logger)) })
}

func withLogger(opts Options, logger *slog.Logger) Options {
	if opts.Logger == nil {
		opts.Logger = logger
	}
	return opts
}

func newModule(constructor any) *gonestly.Module {
	return &gonestly.Module{
		Name:      "cache",
		Global:    true,
		Providers: gonestly.Providers(constructor),
		Exports:   gonestly.Exports(gonestly.Type[*Cache]()),
	}
}

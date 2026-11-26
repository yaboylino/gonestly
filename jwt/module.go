package jwt

import (
	"log/slog"

	"github.com/yaboylino/gonestly"
)

// Module returns a global module that provides *Tokens to every module. Import it once, in
// the root module. Wrong options stop the application from starting.
func Module(opts Options) *gonestly.Module {
	return newModule(func(logger *slog.Logger) (*Tokens, error) { return New(withLogger(opts, logger)) })
}

// ModuleFunc is like Module, but builds the options from a provider of a global module,
// typically the configuration from config.Module:
//
//	jwt.ModuleFunc(func(cfg Config) jwt.Options {
//		return jwt.Options{Secret: cfg.JWTSecret, Issuer: "shop"}
//	})
func ModuleFunc[C any](options func(C) Options) *gonestly.Module {
	return newModule(func(c C, logger *slog.Logger) (*Tokens, error) { return New(withLogger(options(c), logger)) })
}

func withLogger(opts Options, logger *slog.Logger) Options {
	if opts.Logger == nil {
		opts.Logger = logger
	}
	return opts
}

func newModule(constructor any) *gonestly.Module {
	return &gonestly.Module{
		Name:      "jwt",
		Global:    true,
		Providers: gonestly.Providers(constructor),
		Exports:   gonestly.Exports(gonestly.Type[*Tokens]()),
	}
}

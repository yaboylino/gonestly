package password

import "github.com/yaboylino/gonestly"

// Module returns a global module that provides the *Hasher to every module. Import it once, in
// the root module. Wrong options stop the application from starting.
func Module(opts Options) *gonestly.Module {
	return newModule(func() (*Hasher, error) { return New(opts) })
}

// ModuleFunc is like Module, but builds the options from a provider of a global module,
// typically the configuration from config.Module, for example to read the pepper:
//
//	password.ModuleFunc(func(cfg Config) password.Options {
//		return password.Options{Pepper: []byte(cfg.PasswordPepper)}
//	})
func ModuleFunc[C any](options func(C) Options) *gonestly.Module {
	return newModule(func(c C) (*Hasher, error) { return New(options(c)) })
}

func newModule(constructor any) *gonestly.Module {
	return &gonestly.Module{
		Name:      "password",
		Global:    true,
		Providers: gonestly.Providers(constructor),
		Exports:   gonestly.Exports(gonestly.Type[*Hasher]()),
	}
}

package config

import "github.com/yaboylino/gonestly"

// Module returns a global module that loads T with Load when the application starts and
// provides it to every module. A configuration error stops the application from starting.
func Module[T any](opts Options) *gonestly.Module {
	return &gonestly.Module{
		Name:      "config",
		Global:    true,
		Providers: gonestly.Providers(func() (T, error) { return Load[T](opts) }),
		Exports:   gonestly.Exports(gonestly.Type[T]()),
	}
}

package config

import (
	"reflect"

	"github.com/yaboylino/gonestly"
)

// Module returns a global module that loads T with Load when the application starts and
// provides it to every module. A configuration error stops the application from starting.
// The module is named after T, so a feature can load a configuration struct of its own.
func Module[T any](opts Options) *gonestly.Module {
	return &gonestly.Module{
		Name:      "config " + reflect.TypeFor[T]().String(),
		Global:    true,
		Providers: gonestly.Providers(func() (T, error) { return Load[T](opts) }),
		Exports:   gonestly.Exports(gonestly.Type[T]()),
	}
}

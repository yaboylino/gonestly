package cache_test

import (
	"testing"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/cache"
)

type settings struct{ Prefix string }

func TestModules(t *testing.T) {
	config := &gonestly.Module{
		Name:      "settings",
		Global:    true,
		Providers: gonestly.Providers(func() settings { return settings{Prefix: "app:"} }),
		Exports:   gonestly.Exports(gonestly.Type[settings]()),
	}
	for name, module := range map[string]*gonestly.Module{
		"Module": cache.Module(cache.Options{DefaultTTL: time.Minute}),
		"ModuleFunc": cache.ModuleFunc(func(s settings) cache.Options {
			return cache.Options{Prefix: s.Prefix}
		}),
	} {
		var c *cache.Cache
		app, err := gonestly.New(&gonestly.Module{
			Name:      "app",
			Imports:   gonestly.Imports(config, module),
			Providers: gonestly.Providers(func(cc *cache.Cache) *struct{} { c = cc; return &struct{}{} }),
		}, gonestly.WithoutListener())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := cache.Set(t.Context(), c, "k", 1, 0); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if v, found := cache.Get[int](t.Context(), c, "k"); !found || v != 1 {
			t.Errorf("%s: %d, %v", name, v, found)
		}
		if err := app.Stop(t.Context()); err != nil {
			t.Errorf("%s: stopping: %v", name, err)
		}
	}
}

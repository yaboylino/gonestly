package password_test

import (
	"context"
	"testing"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/password"
)

type config struct{ Pepper string }

func TestModules(t *testing.T) {
	settings := &gonestly.Module{
		Name:      "settings",
		Global:    true,
		Providers: gonestly.Providers(func() config { return config{Pepper: "p"} }),
		Exports:   gonestly.Exports(gonestly.Type[config]()),
	}
	for name, module := range map[string]*gonestly.Module{
		"Module": password.Module(password.Options{Memory: 64, Iterations: 1, Parallelism: 1}),
		"ModuleFunc": password.ModuleFunc(func(c config) password.Options {
			return password.Options{Memory: 64, Iterations: 1, Parallelism: 1, Pepper: []byte(c.Pepper)}
		}),
	} {
		var hasher *password.Hasher
		app, err := gonestly.New(&gonestly.Module{
			Name:      "app",
			Imports:   gonestly.Imports(settings, module),
			Providers: gonestly.Providers(func(h *password.Hasher) *struct{} { hasher = h; return &struct{}{} }),
		}, gonestly.WithoutListener())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		hash, err := hasher.Hash(t.Context(), "x")
		if err != nil || hash == "" {
			t.Errorf("%s: %v", name, err)
		}
		_ = app.Stop(context.Background())
	}
}

func TestBadOptionsStopTheApplication(t *testing.T) {
	app, err := gonestly.New(&gonestly.Module{
		Name:      "app",
		Imports:   gonestly.Imports(password.Module(password.Options{MaxConcurrent: -1})),
		Providers: gonestly.Providers(func(*password.Hasher) *struct{} { return &struct{}{} }),
	}, gonestly.WithoutListener())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err == nil {
		t.Error("the application started with bad options")
	}
}

package astedit

import (
	"strings"
	"testing"
)

func TestAddToModule(t *testing.T) {
	tests := []struct {
		name, src, field, expr, importPath, want string
	}{
		{
			name: "single line call",
			src: `package app

import "github.com/yaboylino/gonestly"

// Module is the root module.
var Module = &gonestly.Module{Name: "app", Imports: gonestly.Imports(config.Module)}
`,
			field: "Imports", expr: "users.Module", importPath: "example.com/shop/internal/users",
			want: `package app

import (
	"example.com/shop/internal/users"
	"github.com/yaboylino/gonestly"
)

// Module is the root module.
var Module = &gonestly.Module{Name: "app", Imports: gonestly.Imports(config.Module, users.Module)}
`,
		},
		{
			name: "multi line call with trailing comma",
			src: `package app

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name: "app",
	Imports: gonestly.Imports(
		config.Module, // the configuration
		health.Module,
	),
}
`,
			field: "Imports", expr: "users.Module",
			want: `package app

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name: "app",
	Imports: gonestly.Imports(
		config.Module, // the configuration
		health.Module,
		users.Module,
	),
}
`,
		},
		{
			name: "multi line call without trailing comma",
			src: `package app

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name:      "app",
	Providers: gonestly.Providers(NewA,
		NewB),
}
`,
			field: "Providers", expr: "NewC",
			want: `package app

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name: "app",
	Providers: gonestly.Providers(NewA,
		NewB, NewC),
}
`,
		},
		{
			name: "empty call",
			src: `package users

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name:        "users",
	Controllers: gonestly.Controllers(),
}
`,
			field: "Controllers", expr: "NewController",
			want: `package users

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{
	Name:        "users",
	Controllers: gonestly.Controllers(NewController),
}
`,
		},
		{
			name: "missing field",
			src: `package users

import g "github.com/yaboylino/gonestly"

var Module = &g.Module{
	Name: "users",
}
`,
			field: "Providers", expr: "NewService",
			want: `package users

import g "github.com/yaboylino/gonestly"

var Module = &g.Module{
	Name:      "users",
	Providers: g.Providers(NewService),
}
`,
		},
		{
			name: "slice literal",
			src: `package users

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{Name: "users", Providers: []gonestly.Provider{gonestly.Provide(NewA)}}
`,
			field: "Providers", expr: "gonestly.Provide(NewB)",
			want: `package users

import "github.com/yaboylino/gonestly"

var Module = &gonestly.Module{Name: "users", Providers: []gonestly.Provider{gonestly.Provide(NewA), gonestly.Provide(NewB)}}
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := addToModule("module.go", []byte(tt.src), tt.field, tt.expr, tt.importPath)
			if err != nil || !changed {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
			again, changed, err := addToModule("module.go", got, tt.field, tt.expr, tt.importPath)
			if err != nil || changed || string(again) != string(got) {
				t.Errorf("adding twice changed the file: %v", err)
			}
		})
	}
}

func TestAddToModuleErrors(t *testing.T) {
	for src, want := range map[string]string{
		"package x\n": "no gonestly.Module literal found",
		"package x\nimport \"github.com/yaboylino/gonestly\"\nvar A, B = gonestly.Module{}, gonestly.Module{}\n": "more than one gonestly.Module literal",
		"package x\nimport \"github.com/yaboylino/gonestly\"\nvar Module = &gonestly.Module{Imports: mods}\n":    "cannot add to Imports",
		"package x\nfunc (": "expected",
	} {
		if _, _, err := addToModule("module.go", []byte(src), "Imports", "x.Module", ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	if _, err := AddToModule("module.go", "Nope", "x", ""); err == nil {
		t.Error("an unknown field was accepted")
	}
}

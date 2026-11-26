// Package app wires the modules of the application together.
package app

import (
	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/config"
	"github.com/yaboylino/gonestly/sql"
	"github.com/yaboylino/gonestly/validator"

	"github.com/yaboylino/gonestly/examples/tasks/internal/health"
	"github.com/yaboylino/gonestly/examples/tasks/internal/projects"
	"github.com/yaboylino/gonestly/examples/tasks/internal/tasks"
	"github.com/yaboylino/gonestly/examples/tasks/migrations"
)

// Module is the root module of the application.
var Module = &gonestly.Module{
	Name: "app",
	Imports: gonestly.Imports(
		config.Module[Config](config.Options{}),
		validator.Module(),
		sql.ModuleFunc(func(cfg Config) sql.Options {
			return sql.Options{Driver: "sqlite", DSN: cfg.DatabaseURL, Migrations: migrations.Files}
		}),
		health.Module,
		projects.Module,
		tasks.Module,
	),
	Providers: gonestly.Providers(NewServerConfig, NewOpenAPIConfig),
}

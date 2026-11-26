// Package projects manages projects, which group tasks.
package projects

import (
	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"

	"github.com/yaboylino/gonestly/examples/tasks/internal/auth"
)

// Module provides the routes for projects, and its service to the modules that import it.
var Module = &gonestly.Module{
	Name:        "projects",
	Imports:     gonestly.Imports(auth.Module),
	Providers:   gonestly.Providers(sql.NewRepository[Project, int64], NewService),
	Controllers: gonestly.Controllers(NewController),
	Exports:     gonestly.Exports(NewService),
}

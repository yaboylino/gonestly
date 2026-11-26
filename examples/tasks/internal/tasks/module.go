// Package tasks manages the tasks of projects.
package tasks

import (
	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"

	"github.com/yaboylino/gonestly/examples/tasks/internal/auth"
	"github.com/yaboylino/gonestly/examples/tasks/internal/projects"
)

// Module provides the routes for tasks; it uses the service that the projects module exports.
var Module = &gonestly.Module{
	Name:        "tasks",
	Imports:     gonestly.Imports(auth.Module, projects.Module),
	Providers:   gonestly.Providers(sql.NewRepository[Task, int64], NewService),
	Controllers: gonestly.Controllers(NewController),
}

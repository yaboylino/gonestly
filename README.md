# Gonestly

[![CI](https://github.com/yaboylino/gonestly/actions/workflows/ci.yml/badge.svg)](https://github.com/yaboylino/gonestly/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/yaboylino/gonestly.svg)](https://pkg.go.dev/github.com/yaboylino/gonestly)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-gonestly-fa7807.svg)](https://yaboylino.github.io/gonestly-docs)

A modular application framework for Go, inspired by [NestJS](https://nestjs.com). Gonestly gives you
modules, dependency injection, typed HTTP handlers, a type-safe SQL layer and a CLI that generates
and runs your code, while staying plain, idiomatic Go.

> **Status:** `v0.1.0` is the first release. Until `v1.0.0` a minor version may change the API; the
> [changelog](CHANGELOG.md) lists every change.

**[Read the documentation](https://yaboylino.github.io/gonestly-docs)** in English or Dutch.

## Why Gonestly

- **Structure that scales.** Features live in modules that declare what they need and what they
  share. The whole dependency graph is checked when the application starts, so a missing provider
  or an import cycle fails at boot with a clear message, never in the middle of a request.
- **Plain Go.** No code generation in your handlers, no struct tag magic and no reflection per
  request. A handler is a function with a typed request and a typed response.
- **Built on the standard library.** `net/http`, `log/slog`, `database/sql` and `context`. Any
  existing `net/http` middleware works.
- **SQL that stays out of your way.** Generic repositories for the common cases, a typed query
  builder for the rest, and raw SQL that scans straight into structs when you need full control.
- **A CLI for the boring parts.** Generate a module, a controller or a full CRUD resource, run
  migrations and develop with instant feedback on every save.

## Install

Gonestly needs Go 1.26 or newer. Install the CLI and create a project:

```sh
go install github.com/yaboylino/gonestly/cmd/gonestly@latest
gonestly new shop --database sqlite
cd shop && gonestly dev
```

Or add the framework to an existing module with `go get github.com/yaboylino/gonestly`.

## Preview

```go
// users/module.go
var Module = &gonestly.Module{
	Name:        "users",
	Imports:     gonestly.Imports(database.Module),
	Providers:   gonestly.Providers(NewService, NewRepository),
	Controllers: gonestly.Controllers(NewController),
	Exports:     gonestly.Exports(NewService),
}

// users/controller.go
func (c *Controller) Routes(r *gonestly.Router) {
	r.Prefix("/users").Guard(auth.Required)
	gonestly.Get(r, "/{id}", c.FindOne)
	gonestly.Post(r, "/", c.Create)
}

type FindOneRequest struct {
	ID     string `path:"id"`
	Fields string `query:"fields"`
}

func (c *Controller) FindOne(ctx context.Context, req FindOneRequest) (*User, error) {
	return c.users.FindByID(ctx, req.ID)
}
```

The request is bound from the path, query, headers and JSON body, validated, and the result is
sent as JSON. Errors such as `gonestly.NotFound("user %s does not exist", id)` become problem
details (RFC 9457).

Every route is described in an OpenAPI 3.1 document, generated from the handler types, the
struct tags and the guards, and served at `/openapi.json` with Swagger UI at `/docs`. There
is nothing to annotate.

Typed configuration from environment variables and `.env` files, checked when the application
starts:

```go
type Config struct {
	Port        int    `default:"3000"`
	DatabaseURL string `env:"DATABASE_URL,required"`
}

var AppModule = &gonestly.Module{
	Name:    "app",
	Imports: gonestly.Imports(config.Module[Config](config.Options{}), users.Module),
}
```

Querying with the SQL layer, from a generic repository to typed queries and raw SQL:

```go
users, err := sql.NewRepository[User, int64](db)
user, err := users.FindByID(ctx, 42) // sql.ErrNotFound answers 404 from a handler

var email, createdAt = sql.Col[string]("email"), sql.Col[time.Time]("created_at")
recent, err := sql.From[User](db).
	Where(email.Like("%@example.com")).
	OrderBy(createdAt.Desc()).
	Limit(20).
	All(ctx)

total, err := sql.QueryOne[int64](ctx, db, "SELECT COUNT(*) FROM users WHERE team_id IN (?)", teamIDs)
```

Transactions travel through the context, so repositories join them automatically, and
migrations are plain SQL files applied when the application starts.

Developing with the CLI:

```sh
gonestly new shop --database postgres
cd shop
gonestly generate resource products
gonestly dev
```

`gonestly dev` watches your project and checks it on every save: it builds the code and runs
`go vet`, and with `--test` also the tests of the packages you changed. When everything passes,
the application is restarted gracefully. When something fails, the application is stopped and
every problem is listed with its file, line and message, so a broken change never looks like it
works. The application starts again as soon as the problems are fixed.

The CLI also generates modules, controllers, services, guards, interceptors and the typed
columns of your models, manages migrations, and lists the routes and the OpenAPI document of
your application.

## Roadmap

- [x] Core: modules, dependency injection, lifecycle hooks, configuration and logging
- [x] HTTP: router, typed handlers, binding, validation, guards, interceptors and error handling
- [x] SQL: repositories, typed query builder, transactions and migrations for PostgreSQL, MySQL and SQLite
- [x] OpenAPI: a document and Swagger UI generated from every route
- [x] CLI: `new`, `generate`, `dev` with live rebuild and restart, `migrate`, `routes` and `openapi`
- [x] Documentation site in English and Dutch
- [x] MySQL and MariaDB support
- [x] First release: `v0.1.0`

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started, and please follow
the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## License

Gonestly is released under the [MIT License](LICENSE).

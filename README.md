# Gonestly

[![CI](https://github.com/yaboylino/gonestly/actions/workflows/ci.yml/badge.svg)](https://github.com/yaboylino/gonestly/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/yaboylino/gonestly.svg)](https://pkg.go.dev/github.com/yaboylino/gonestly)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A modular application framework for Go, inspired by [NestJS](https://nestjs.com). Gonestly gives you
modules, dependency injection, typed HTTP handlers, a type-safe SQL layer and a CLI that generates
and runs your code, while staying plain, idiomatic Go.

> **Status:** in active development. The API below is a preview and will change before `v0.1.0`.

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

func (c *Controller) FindOne(ctx context.Context, req FindOneRequest) (*User, error) {
	return c.users.FindByID(ctx, req.ID)
}
```

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

Querying with the typed builder:

```go
users, err := sql.From[User](db).
	Where(user.Email.Like("%@example.com"), user.Active.Eq(true)).
	OrderBy(user.CreatedAt.Desc()).
	Limit(20).
	All(ctx)
```

Developing with the CLI:

```sh
gonestly new shop
cd shop
gonestly generate resource products
gonestly dev
```

`gonestly dev` watches your project and checks it on every save: it builds the code and runs
`go vet`, and with `--test` also the tests of the packages you changed. When everything passes,
the application is restarted gracefully. When something fails, the application is stopped and
every problem is listed with its file, line and message, so a broken change never looks like it
works. The application starts again as soon as the problems are fixed.

## Roadmap

- [ ] Core: modules, dependency injection, lifecycle hooks, configuration and logging
- [ ] HTTP: router, typed handlers, binding, validation, guards, interceptors and error handling
- [ ] SQL: repositories, typed query builder, transactions and migrations for PostgreSQL, SQLite and MySQL
- [ ] CLI: `new`, `generate`, `dev` with live rebuild and restart, `migrate` and `routes`
- [ ] OpenAPI generation, documentation site and examples
- [ ] First release: `v0.1.0`

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) to get started, and please follow
the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Please report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## License

Gonestly is released under the [MIT License](LICENSE).

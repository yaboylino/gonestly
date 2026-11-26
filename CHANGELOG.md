# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Relations between models: a field tagged `rel` holds the related rows, and `Include`,
  `Repository.Load` and `LoadAll` fill them with one query per relation, nested ones
  included, however many rows there are. `gonestly generate columns` writes a typed value for
  every relation, and `Repository.CreateNested` inserts a row with the rows in its relation
  fields in one transaction.
- `gonestly add database`, which asks for the type of database (SQLite, MySQL or PostgreSQL)
  and for the host, port, user, password and name, creates the database on its server and
  connects the project to it: the connection string in `.env`, the sql module in the root
  module, the driver and a migrations directory. Without a terminal every detail comes from
  a flag and the password from `GONESTLY_DB_PASSWORD`.

### Changed

- A relation pointer whose key column exists in both models, such as a parent in the same
  table, is ambiguous: add `,belongs` or `,has` to the `rel` tag. The application reports it
  when it starts instead of guessing a direction.
- `gonestly add database` connects to a PostgreSQL or MySQL server on another host with TLS
  by default (`--ssl` changes it), restores every file it changed when a step fails, creates a
  relative SQLite path in the project, and adds the sql module at the version of the framework
  that the project requires.

### Fixed

- `CreateNested` returns an error for a model without a primary key instead of panicking, and
  puts back the keys it had set when the insert is rolled back.
- `Include` reports a relation declared for the wrong type instead of panicking.
- `gonestly generate columns` refuses relation fields that are arrays or hold pointers.
- The default database name skips a major version suffix such as `/v2`.

## [0.2.0] - 2026-11-26

### Added

- `ServerConfig.CORS`, which lets pages on other origins call the API: it answers preflight
  requests and reports a mistake in the configuration when the application starts. New
  projects read the origins from `CORS_ORIGINS`.
- Forms and file uploads: the `form` tag reads a field from a URL encoded or multipart form,
  and a field of type `File`, `*File` or `[]File` receives uploaded files. Forms are described
  in the OpenAPI document, and `gonestlytest` sends them with `url.Values` and `Form`.
- `MaxBodyBytes`, a route option that sets the largest request body of one route.
- `RateLimit`, middleware that answers 429 when a client sends too many requests, with the
  `RateLimit` headers and `Retry-After`.
- The `jwt` module: `Tokens` signs and verifies JSON Web Tokens with a secret, a key pair or
  the keys of an identity provider, and its guard puts the claims in the context and can
  require a role.
- `Repository.CreateAll`, `Upsert` and `UpsertAll`: many rows in few statements, and rows that
  are inserted or updated by their primary key or by unique columns.
- `Health`, which every module can inject to report whether the application can handle
  requests. It asks every provider that implements `HealthChecker`, such as `*sql.DB`, and
  reports that the application is unavailable while it stops. New projects serve
  `/health/live` and `/health/ready`.
- `WithShutdownDelay`, which keeps the server answering for a while after a stop signal, so
  that traffic can move elsewhere first.
- `examples/tasks`, a complete application with two feature modules, a guard, transactions
  across modules and tests.

### Changed

- New projects serve `/health/live` and `/health/ready` instead of `/health`.

## [0.1.0] - 2026-11-26

### Added

- Modules with imports, exports and global modules.
- Dependency injection through constructors, with `Provide`, `As` for interfaces, `Value` for
  existing values and `Alias` to share one instance under two types.
- Validation of the whole module graph when the application is built, reporting every missing
  dependency, cycle and declaration error at once with a hint on how to fix it.
- Application lifecycle with `Starter` and `Stopper` hooks, graceful shutdown on interrupt and
  termination signals, and `Shutdowner` to stop the application from a provider.
- `Override` to replace providers in tests, and `Resolve` to get a provider from a running
  application.
- The `config` package: typed configuration from environment variables and dotenv files.
- Controllers with typed handlers registered with `Get`, `Post`, `Put`, `Patch`, `Delete` and
  `Handle`, and a router with prefixes, groups, guards, interceptors and net/http middleware.
- Request binding from path values, query parameters, headers and the JSON body, with checks
  at startup that every path value exists in its route.
- Validation through a `Validate` method on the request, or with struct tags through the new
  `validator` module.
- HTTP errors such as `NotFound` and `Invalid`, rendered as problem details (RFC 9457); other
  errors become a 500 whose cause is logged but never sent.
- An HTTP server with secure timeouts and a body size limit, configured with `ServerConfig` and
  shut down gracefully with the application.
- The `gonestlytest` package to test applications without opening a port.
- The `sql` module: a generic `Repository`, a typed query builder with `From` and `Col`, and
  `Query` and `QueryOne` that scan raw SQL into structs, for PostgreSQL, MySQL, MariaDB and SQLite.
- Transactions through the context, with savepoints for nested transactions.
- Migrations from SQL files with checksums, a lock on PostgreSQL and MySQL and one transaction each.
- `sql.ErrNotFound`, which answers 404 when a handler returns it.
- An OpenAPI 3.1 document generated for every route, from the request and response types,
  the `json`, `validate`, `doc` and `example` tags and the security schemes of guards, served
  with Swagger UI and configured with `OpenAPIConfig`.
- The route options `Summary`, `Description`, `Tags`, `OperationID` and `Deprecated`, and
  `SecuredGuard` for guards that describe how clients authenticate.
- The `gonestly` CLI: `new` creates a project that runs immediately, `generate` creates
  resources, modules, controllers, services, guards, interceptors and typed columns and
  registers them, `dev` rebuilds and restarts the application on every change and stops it
  when a change breaks the build, vet or tests, `migrate` applies, reverts, lists and creates
  migrations, and `routes` and `openapi` inspect the application.
- `InspectEnv`, which makes `App.Run` print the routes or the OpenAPI document instead of
  serving, and `InspectOutputEnv` to write them to a file.
- `RequestID` and `AccessLog`, middleware that gives every request an ID and a log line; the
  ID is also added to the error logs of the application.
- `sql.ErrConflict`, which answers 409 when a unique or primary key value already exists.
- `sql.ReadOnly` and `sql.Isolation`, options for `DB.Transaction`.
- Migrations that run outside a transaction with the comment `-- gonestly:no-transaction`,
  for statements such as `CREATE INDEX CONCURRENTLY`.
- Pointer fields such as `*int` for path, query, header and configuration values, which stay
  nil when the value is absent.
- Quoted values that span several lines in dotenv files.

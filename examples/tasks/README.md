# Tasks

An example API built with [Gonestly](https://github.com/yaboylino/gonestly): projects with tasks,
on SQLite, with a token for everything that changes data.

## Run it

```sh
cp .env.example .env
go run .
```

The API listens on http://localhost:3000, and http://localhost:3000/docs shows every route in
Swagger UI. The database is created and migrated when the application starts.

```sh
curl -X POST localhost:3000/projects -H 'Authorization: Bearer change-me' \
  -H 'Content-Type: application/json' -d '{"slug": "website", "name": "New website"}'
curl -X POST localhost:3000/tasks -H 'Authorization: Bearer change-me' \
  -H 'Content-Type: application/json' -d '{"projectId": 1, "title": "Write the home page"}'
curl 'localhost:3000/tasks?project=1&done=false'
```

With the [CLI](https://yaboylino.github.io/gonestly-docs/en/cli), `gonestly dev` restarts the
application on every change, and `gonestly routes` lists the routes.

## What it shows

| Where                       | What                                                                                         |
| --------------------------- | -------------------------------------------------------------------------------------------- |
| `internal/app`              | The root module: configuration, validation, the database and the feature modules.            |
| `internal/auth`             | A module with its own configuration that exports a guard, described in the OpenAPI document. |
| `internal/projects`         | A repository, a typed query, a 409 for a slug that is taken, and an exported service.        |
| `internal/tasks`            | A module that imports another one, optional filters, and transactions across both modules.   |
| `internal/tasks/service.go` | `Create` stores a task and updates its project together or not at all.                       |
| `internal/app/app_test.go`  | Tests of the whole application, without opening a port.                                      |
| `migrations`                | SQL migrations, embedded in the binary and applied at startup.                               |

The transaction in `tasks.Service.Create` calls `projects.Service.AdjustOpenTasks`. The projects
service knows nothing about that transaction: it receives the context, and its query joins in.

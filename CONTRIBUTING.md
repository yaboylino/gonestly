# Contributing to Gonestly

Thank you for helping out. This guide explains how to set up the project and what a good pull
request looks like.

## Before you start

For anything larger than a small fix, open an issue first so we can agree on the approach before
you spend time on it. Bug reports need a minimal reproduction; feature requests need the problem
they solve.

## Development setup

You need Go 1.26 or newer. All other tools run through `go run` at a pinned version, so there is
nothing else to install.

```sh
git clone https://github.com/yaboylino/gonestly.git
cd gonestly
make check
```

| Command      | What it does                                         |
| ------------ | ---------------------------------------------------- |
| `make check` | Runs every check that CI runs                         |
| `make test`  | Runs the tests with the race detector                 |
| `make cover` | Runs the tests and opens the coverage report          |
| `make lint`  | Runs golangci-lint                                    |
| `make fmt`   | Formats the code with gofumpt and goimports           |
| `make vuln`  | Scans the dependencies with govulncheck               |
| `make tidy`  | Verifies that `go.mod` and `go.sum` are tidy          |

## Guidelines

- **Keep the core dependency free.** The root module only uses the standard library. Integrations
  with third party packages live in their own module, such as `validator/`, which `make` checks
  along with the root module.
- **Fail at startup, not at runtime.** Configuration and wiring errors should surface when the
  application boots, with a message that says what is wrong and how to fix it.
- **Test what you change.** New behavior comes with tests; bug fixes come with a test that fails
  without the fix.
- **Document the public API.** Every exported identifier has a doc comment, and user facing changes
  are listed in `CHANGELOG.md` under "Unreleased".

## Commits and pull requests

Pull requests are squash merged, so the pull request title becomes the commit message on `main`.
It must follow [Conventional Commits](https://www.conventionalcommits.org):

```text
feat(sql): add support for upserts
fix(http): return 415 for an unsupported content type
docs: explain module exports
```

Use `feat!:` or a `BREAKING CHANGE:` footer for breaking changes.

## Releasing

The root module is tagged as `vX.Y.Z` and each nested module as `<dir>/vX.Y.Z`, such as
`validator/v0.1.0`. Before tagging a nested module, replace its `replace` directive with a
`require` of the released root version.

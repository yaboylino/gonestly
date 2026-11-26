GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.10.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Every Go module in the repository; integrations have their own because of their dependencies.
MODULES := . validator sql jwt jobs password cache redis cmd/gonestly examples/tasks

# The SQL tests also run against PostgreSQL and MySQL when GONESTLY_TEST_POSTGRES and
# GONESTLY_TEST_MYSQL are set; `make databases` starts matching databases in Docker.
POSTGRES_DSN := postgres://postgres:gonestly@127.0.0.1:55432/gonestly?sslmode=disable
MYSQL_DSN := root:gonestly@tcp(127.0.0.1:53306)/gonestly
REDIS_URL := redis://127.0.0.1:56379/0

.PHONY: check test test-databases databases cover lint fmt vuln tidy

# check: run every check that CI runs
check: tidy lint test vuln

# test: run the tests with the race detector
test:
	@for m in $(MODULES); do (cd $$m && go test -race -shuffle=on ./...) || exit 1; done

# test-databases: run the tests, including the SQL tests against the databases of `make databases`
test-databases:
	GONESTLY_TEST_POSTGRES="$(POSTGRES_DSN)" GONESTLY_TEST_MYSQL="$(MYSQL_DSN)" GONESTLY_TEST_REDIS="$(REDIS_URL)" $(MAKE) test

# databases: start PostgreSQL, MySQL and Redis containers for the tests
databases:
	docker run -d --rm --name gonestly-test-pg -e POSTGRES_PASSWORD=gonestly -e POSTGRES_DB=gonestly -p 55432:5432 postgres:17-alpine
	docker run -d --rm --name gonestly-test-mysql -e MYSQL_ROOT_PASSWORD=gonestly -e MYSQL_DATABASE=gonestly -p 53306:3306 mysql:8.4
	docker run -d --rm --name gonestly-test-redis -p 56379:6379 redis:7-alpine

# cover: run the tests and open the coverage report
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# lint: run the linters
lint:
	@for m in $(MODULES); do (cd $$m && $(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml) || exit 1; done

# fmt: format the code
fmt:
	@for m in $(MODULES); do (cd $$m && $(GOLANGCI_LINT) fmt --config $(CURDIR)/.golangci.yml) || exit 1; done

# vuln: scan for known vulnerabilities
vuln:
	@for m in $(MODULES); do (cd $$m && $(GOVULNCHECK) ./...) || exit 1; done

# tidy: verify that go.mod and go.sum are tidy
tidy:
	@for m in $(MODULES); do (cd $$m && go mod tidy -diff) || exit 1; done

GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.10.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

.PHONY: check test cover lint fmt vuln tidy

# check: run every check that CI runs
check: tidy lint test vuln

# test: run the tests with the race detector
test:
	go test -race -shuffle=on ./...

# cover: run the tests and open the coverage report
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# lint: run the linters
lint:
	$(GOLANGCI_LINT) run

# fmt: format the code
fmt:
	$(GOLANGCI_LINT) fmt

# vuln: scan for known vulnerabilities
vuln:
	$(GOVULNCHECK) ./...

# tidy: verify that go.mod and go.sum are tidy
tidy:
	go mod tidy -diff

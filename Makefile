GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.10.0

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

# Every Go module in the repository; the validator has its own because of its dependency.
MODULES := . validator

.PHONY: check test cover lint fmt vuln tidy

# check: run every check that CI runs
check: tidy lint test vuln

# test: run the tests with the race detector
test:
	@for m in $(MODULES); do (cd $$m && go test -race -shuffle=on ./...) || exit 1; done

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
